// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/kolonuk/syntaxis/internal/provider"
)

// EstimateTokens is a deliberately conservative estimator (over-counts) used
// when the provider does not report usage.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// ~3 bytes per token is pessimistic for English, realistic for code.
	return (len(s) + 2) / 3
}

// MessageTokens estimates one message including framing overhead.
func MessageTokens(m provider.Message) int {
	n := 4 + EstimateTokens(m.Content)
	for _, tc := range m.ToolCalls {
		n += EstimateTokens(tc.Name) + EstimateTokens(string(tc.Arguments)) + 8
	}
	return n
}

// TotalTokens sums a slice.
func TotalTokens(ms []provider.Message) int {
	n := 0
	for _, m := range ms {
		n += MessageTokens(m)
	}
	return n
}

// TruncateOutput keeps the head and tail of long tool output and says how
// much was omitted. The full output stays in history/UI; only the model's copy
// is trimmed.
func TruncateOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	lines := strings.Split(s, "\n")
	headBudget, tailBudget := maxBytes*6/10, maxBytes*4/10
	var head, tail []string
	used := 0
	i := 0
	for ; i < len(lines); i++ {
		if used+len(lines[i])+1 > headBudget {
			break
		}
		used += len(lines[i]) + 1
		head = append(head, lines[i])
	}
	used = 0
	j := len(lines) - 1
	for ; j > i; j-- {
		if used+len(lines[j])+1 > tailBudget {
			break
		}
		used += len(lines[j]) + 1
		tail = append([]string{lines[j]}, tail...)
	}
	omitted := j - i + 1
	if len(head)+len(tail) == 0 || omitted <= 0 { // a single very long line: cut by bytes
		h, t := maxBytes*6/10, maxBytes*4/10
		return s[:h] + fmt.Sprintf("\n[... %d bytes omitted; use grep/sed to look closer ...]\n", len(s)-h-t) + s[len(s)-t:]
	}
	return strings.Join(head, "\n") + fmt.Sprintf("\n[... %d lines omitted; use grep/sed to look closer ...]\n", omitted) + strings.Join(tail, "\n")
}

// Budget is the token budget for one request.
type Budget struct {
	Window    int     // the REAL context window of the model (not the advertised maximum)
	Reserve   int     // tokens reserved for the completion
	CompactAt float64 // fraction of the usable window that triggers compaction (default 0.75)
}

// Usable returns the prompt budget: leave headroom for the reply and a safety margin.
func (b Budget) Usable() int {
	margin := b.Window / 20 // 5% safety for estimator error
	u := b.Window - b.Reserve - margin
	if u < 0 {
		return 0
	}
	return u
}

// CompactThreshold returns the prompt size that triggers compaction.
func (b Budget) CompactThreshold() int {
	f := b.CompactAt
	if f <= 0 || f > 1 {
		f = 0.75
	}
	return int(float64(b.Usable()) * f)
}

// Meter is shown in the UI.
type Meter struct {
	Used, Window, CompactAt int
	Percent                 int
}

// MeterFor computes the context meter for a prompt of the given size.
func (b Budget) MeterFor(used int) Meter {
	p := 0
	if b.Usable() > 0 {
		p = used * 100 / b.Usable()
	}
	return Meter{Used: used, Window: b.Window, CompactAt: b.CompactThreshold(), Percent: p}
}

// groupMessages splits messages into units that must stay together: an
// assistant message with tool calls and the tool results that answer it.
func groupMessages(ms []provider.Message) [][]provider.Message {
	var groups [][]provider.Message
	for i := 0; i < len(ms); i++ {
		g := []provider.Message{ms[i]}
		if ms[i].Role == "assistant" && len(ms[i].ToolCalls) > 0 {
			for i+1 < len(ms) && ms[i+1].Role == "tool" {
				g = append(g, ms[i+1])
				i++
			}
		}
		groups = append(groups, g)
	}
	return groups
}

// Summarizer condenses older turns into a running note.
type Summarizer func(ctx context.Context, prevSummary string, older []provider.Message) (string, error)

// CompactResult is the outcome of compaction.
type CompactResult struct {
	Summary   string
	Kept      []provider.Message
	Dropped   int
	Compacted bool
}

// Compact summarises everything except the last keepLast groups. The caller
// stores the summary; the full transcript is never discarded from history.
func Compact(ctx context.Context, msgs []provider.Message, prevSummary string, keepLast int, sum Summarizer) (CompactResult, error) {
	groups := groupMessages(msgs)
	if keepLast < 1 {
		keepLast = 1
	}
	if len(groups) <= keepLast {
		return CompactResult{Summary: prevSummary, Kept: msgs}, nil
	}
	cut := len(groups) - keepLast
	var older []provider.Message
	for _, g := range groups[:cut] {
		older = append(older, g...)
	}
	var kept []provider.Message
	for _, g := range groups[cut:] {
		kept = append(kept, g...)
	}
	s, err := sum(ctx, prevSummary, older)
	if err != nil {
		return CompactResult{}, err
	}
	return CompactResult{Summary: s, Kept: kept, Dropped: len(older), Compacted: true}, nil
}

// SummaryPrompt is the instruction given to the summarising model.
const SummaryPrompt = `Summarise the work so far as a running note for yourself. Use exactly these sections:
## What has been done
## What is left
## Key files and decisions
## Open problems / errors
Be concrete (file paths, commands, test names). Do not invent anything. Keep it under 400 words.`

// Assemble builds the prompt for one request: system prompt + pinned rules +
// running summary + as many recent groups as fit the budget. The first user
// message is always kept (the original task). Returns the messages and how
// many history messages were left out.
func Assemble(system, pinned, summary string, history []provider.Message, b Budget) ([]provider.Message, int) {
	var head []provider.Message
	sys := system
	if pinned != "" {
		sys += "\n\n" + pinned
	}
	head = append(head, provider.Message{Role: "system", Content: sys})
	if summary != "" {
		head = append(head, provider.Message{Role: "user", Content: "Summary of earlier work in this session (older messages were compacted):\n" + summary})
	}
	budget := b.Usable() - TotalTokens(head)
	// reserve room for the original task when no summary covers it
	var first *provider.Message
	if summary == "" {
		for i := range history {
			if history[i].Role == "user" {
				first = &history[i]
				break
			}
		}
	}
	reserved := 0
	if first != nil {
		reserved = MessageTokens(*first)
	}
	groups := groupMessages(history)
	var chosen [][]provider.Message
	used := 0
	for i := len(groups) - 1; i >= 0; i-- {
		t := TotalTokens(groups[i])
		if used+t > budget-reserved && len(chosen) > 0 {
			break
		}
		used += t
		chosen = append([][]provider.Message{groups[i]}, chosen...)
	}
	out := append([]provider.Message(nil), head...)
	count := 0
	includesFirst := false
	for _, g := range chosen {
		for _, m := range g {
			if first != nil && m.Content == first.Content && m.Role == "user" {
				includesFirst = true
			}
		}
		count += len(g)
	}
	if first != nil && !includesFirst {
		out = append(out, *first)
		count++
	}
	for _, g := range chosen {
		out = append(out, g...)
	}
	return out, len(history) - count
}
