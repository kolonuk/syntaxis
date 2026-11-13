// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/provider"
)

func TestEstimateIsConservative(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty")
	}
	s := strings.Repeat("word ", 1000) // 5000 bytes; real tokenisers give ~1000-1300
	if EstimateTokens(s) < 1000 {
		t.Fatalf("estimator under-counts: %d", EstimateTokens(s))
	}
}

func TestTruncateOutputHeadTail(t *testing.T) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, fmt.Sprintf("line %04d", i))
	}
	in := strings.Join(lines, "\n")
	out := TruncateOutput(in, 500)
	if len(out) > 700 {
		t.Fatalf("not truncated: %d", len(out))
	}
	if !strings.Contains(out, "line 0000") || !strings.Contains(out, "line 0999") {
		t.Fatal("head/tail lost")
	}
	if !strings.Contains(out, "lines omitted; use grep/sed to look closer") {
		t.Fatalf("no omission note: %s", out)
	}
	if strings.Contains(out, "line 0500") {
		t.Fatal("middle kept")
	}
	if TruncateOutput("short", 500) != "short" || TruncateOutput(in, 0) != in {
		t.Fatal("pass-through")
	}
}

func TestTruncateSingleHugeLine(t *testing.T) {
	in := strings.Repeat("x", 10000)
	out := TruncateOutput(in, 1000)
	if len(out) > 1200 || !strings.Contains(out, "bytes omitted") {
		t.Fatalf("%d %q", len(out), out[:50])
	}
}

func TestBudgetMath(t *testing.T) {
	b := Budget{Window: 8000, Reserve: 1000}
	if b.Usable() != 8000-1000-400 {
		t.Fatalf("%d", b.Usable())
	}
	if b.CompactThreshold() != int(float64(6600)*0.75) {
		t.Fatalf("%d", b.CompactThreshold())
	}
	if (Budget{Window: 100, Reserve: 500}).Usable() != 0 {
		t.Fatal("negative usable")
	}
	m := b.MeterFor(3300)
	if m.Percent != 50 || m.Window != 8000 {
		t.Fatalf("%+v", m)
	}
	if (Budget{}).MeterFor(10).Percent != 0 {
		t.Fatal("zero budget")
	}
}

func msg(role, content string) provider.Message {
	return provider.Message{Role: role, Content: content}
}

func convo(n int) []provider.Message {
	var ms []provider.Message
	for i := 0; i < n; i++ {
		ms = append(ms, msg("user", fmt.Sprintf("task step %d %s", i, strings.Repeat("a", 90))))
		ms = append(ms, provider.Message{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: fmt.Sprint(i), Name: "run_shell", Arguments: []byte(`{"command":"ls"}`)}}})
		ms = append(ms, provider.Message{Role: "tool", ToolCallID: fmt.Sprint(i), Content: strings.Repeat("o", 90)})
	}
	return ms
}

func TestGroupsKeepToolPairsTogether(t *testing.T) {
	gs := groupMessages(convo(3))
	if len(gs) != 6 {
		t.Fatalf("%d groups", len(gs))
	}
	if len(gs[1]) != 2 || gs[1][0].Role != "assistant" || gs[1][1].Role != "tool" {
		t.Fatalf("%+v", gs[1])
	}
}

func TestAssembleNeverExceedsWindow(t *testing.T) {
	hist := convo(200)
	b := Budget{Window: 2000, Reserve: 300}
	out, dropped := Assemble("SYSTEM", "RULES", "", hist, b)
	if TotalTokens(out) > b.Usable() {
		t.Fatalf("prompt %d exceeds usable %d", TotalTokens(out), b.Usable())
	}
	if dropped == 0 || out[0].Role != "system" || !strings.Contains(out[0].Content, "RULES") {
		t.Fatalf("dropped=%d", dropped)
	}
	// the most recent group is always included
	last := hist[len(hist)-1]
	if out[len(out)-1].Content != last.Content {
		t.Fatal("latest message missing")
	}
	// no tool message without its assistant call
	for i, m := range out {
		if m.Role == "tool" && (i == 0 || (out[i-1].Role != "assistant" && out[i-1].Role != "tool")) {
			t.Fatalf("orphan tool message at %d", i)
		}
	}
}

func TestAssembleKeepsOriginalTask(t *testing.T) {
	hist := append([]provider.Message{msg("user", "ORIGINAL TASK")}, convo(100)[1:]...)
	out, _ := Assemble("S", "", "", hist, Budget{Window: 1500, Reserve: 200})
	if !strings.Contains(out[1].Content, "ORIGINAL TASK") && !strings.Contains(out[1].Content, "task step") {
		t.Fatalf("%+v", out[1])
	}
	found := false
	for _, m := range out {
		if m.Content == "ORIGINAL TASK" {
			found = true
		}
	}
	if !found {
		t.Fatal("original task dropped with no summary")
	}
}

func TestAssembleWithSummaryAndSmallHistory(t *testing.T) {
	out, dropped := Assemble("S", "", "did X; left Y", convo(1), Budget{Window: 100000, Reserve: 1000})
	if dropped != 0 || len(out) != 1+1+3 || !strings.Contains(out[1].Content, "did X; left Y") {
		t.Fatalf("dropped=%d %+v", dropped, out)
	}
}

func TestCompact(t *testing.T) {
	hist := convo(10)
	var seenOlder int
	var seenPrev string
	sum := func(_ context.Context, prev string, older []provider.Message) (string, error) {
		seenOlder, seenPrev = len(older), prev
		return "SUMMARY of " + fmt.Sprint(len(older)), nil
	}
	r, err := Compact(context.Background(), hist, "previous note", 3, sum)
	if err != nil || !r.Compacted {
		t.Fatalf("%v %+v", err, r)
	}
	if seenPrev != "previous note" || seenOlder != len(hist)-r.Dropped+r.Dropped-len(r.Kept) {
		t.Fatalf("older=%d prev=%q", seenOlder, seenPrev)
	}
	// kept = last 3 groups; tool pairing intact
	if len(r.Kept) != 5 {
		t.Fatalf("kept %d", len(r.Kept))
	}
	if r.Kept[0].Role == "tool" {
		t.Fatal("compaction split a tool pair")
	}
	if r.Dropped+len(r.Kept) != len(hist) {
		t.Fatal("messages lost")
	}
}

func TestCompactNothingToDoAndError(t *testing.T) {
	r, err := Compact(context.Background(), convo(1), "s", 5, func(context.Context, string, []provider.Message) (string, error) {
		t.Fatal("summariser called needlessly")
		return "", nil
	})
	if err != nil || r.Compacted || r.Summary != "s" {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = Compact(context.Background(), convo(10), "", 2, func(context.Context, string, []provider.Message) (string, error) {
		return "", errors.New("model down")
	})
	if err == nil {
		t.Fatal("summariser error swallowed")
	}
}
