// SPDX-License-Identifier: GPL-2.0-or-later

package bench

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

type mem struct {
	mu sync.Mutex
	f  map[string]string
}

func (m *mem) exec(_ context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := e.Cmd
	switch {
	case len(c) >= 3 && c[0] == "cat" && c[1] == "--":
		if v, ok := m.f[c[2]]; ok {
			return sandbox.ExecResult{Stdout: v}, nil
		}
		return sandbox.ExecResult{ExitCode: 1, Stderr: "no such file"}, nil
	case len(c) >= 5 && c[0] == "sh" && strings.Contains(c[2], "cat > "):
		m.f[c[4]] = string(e.Stdin)
		return sandbox.ExecResult{}, nil
	case len(c) >= 5 && c[0] == "sh" && strings.HasPrefix(c[2], "cat --"):
		if v, ok := m.f[c[4]]; ok {
			return sandbox.ExecResult{Stdout: v}, nil
		}
		return sandbox.ExecResult{ExitCode: 1}, nil
	}
	return sandbox.ExecResult{Stdout: "ok"}, nil
}

// goodModel answers every task correctly, in the way the tools expect.
func goodModel(t *testing.T) func(context.Context, provider.ChatRequest, func(string)) (*provider.ChatResult, error) {
	return func(_ context.Context, req provider.ChatRequest, _ func(string)) (*provider.ChatResult, error) {
		last := req.Messages[len(req.Messages)-1]
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		call := func(name, args string) *provider.ChatResult {
			return &provider.ChatResult{ToolCalls: []provider.ToolCall{{ID: "c" + name, Name: name, Arguments: json.RawMessage(args)}},
				Usage: provider.Usage{PromptTokens: 100, CompletionTokens: 20, Reported: true}, TTFT: 100 * time.Millisecond, Duration: time.Second}
		}
		done := func(s string) *provider.ChatResult {
			return &provider.ChatResult{Content: s, Usage: provider.Usage{PromptTokens: 120, CompletionTokens: 10, Reported: true}, TTFT: 100 * time.Millisecond, Duration: time.Second}
		}
		switch {
		case strings.Contains(user, "hello.txt"):
			if last.Role == "tool" {
				return done("done"), nil
			}
			return call("write_file", `{"path":"/workspace/hello.txt","content":"hello syntaxis"}`), nil
		case strings.Contains(user, "config.txt"):
			if last.Role == "tool" && strings.Contains(last.Content, "wrote") || last.Role == "tool" && strings.Contains(last.Content, "patched") {
				return done("done"), nil
			}
			return call("patch_file", `{"path":"/workspace/config.txt","old":"port=8080","new":"port=9090"}`), nil
		case strings.Contains(user, "ANSWER"):
			return done("ANSWER: 42"), nil
		case strings.Contains(user, "sorted.txt"):
			if last.Role == "tool" {
				return done("6"), nil
			}
			return call("write_file", `{"path":"/workspace/sorted.txt","content":"1\n2\n3\n"}`), nil
		}
		return done("?"), nil
	}
}

func env(t *testing.T, chat func(context.Context, provider.ChatRequest, func(string)) (*provider.ChatResult, error)) (Env, *mem) {
	m := &mem{f: map[string]string{}}
	return Env{Model: "some-model", Digest: "d1", Window: 8192, Chat: chat, Exec: m.exec}, m
}

func TestSuiteWithCompetentModelPasses(t *testing.T) {
	e, _ := env(t, goodModel(t))
	rep := Run(context.Background(), e, QuickSuite())
	for _, tr := range rep.Tasks {
		if !tr.Passed {
			t.Errorf("task %s failed: %s (%s)", tr.ID, tr.Detail, tr.Status)
		}
	}
	if rep.PassRate != 1 || rep.TokensPerSec <= 0 || rep.TTFTMillis != 100 || rep.ToolFailRate != 0 || rep.MaxPromptUsed == 0 {
		t.Fatalf("%+v", rep)
	}
	if !strings.Contains(rep.ContextNote, "peak prompt") {
		t.Fatal(rep.ContextNote)
	}
	if !json.Valid([]byte(rep.JSON())) {
		t.Fatal("json")
	}
}

func TestIncompetentModelFails(t *testing.T) {
	e, _ := env(t, func(context.Context, provider.ChatRequest, func(string)) (*provider.ChatResult, error) {
		return &provider.ChatResult{Content: "I think the answer is 42.", Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 5, Reported: true}}, nil
	})
	rep := Run(context.Background(), e, QuickSuite())
	if rep.PassRate != 0 {
		t.Fatalf("a model that does nothing must not pass: %+v", rep)
	}
}

func TestToolFailureRateCounted(t *testing.T) {
	n := 0
	e, _ := env(t, func(context.Context, provider.ChatRequest, func(string)) (*provider.ChatResult, error) {
		n++
		if n%2 == 1 {
			return &provider.ChatResult{ToolCalls: []provider.ToolCall{{ID: "x", Name: "read_file", Arguments: json.RawMessage(`{"path":"/etc/passwd"}`)}}}, nil
		}
		return &provider.ChatResult{Content: "gave up"}, nil
	})
	rep := Run(context.Background(), e, QuickSuite()[:1])
	if rep.ToolFailRate != 1 {
		t.Fatalf("a rejected tool call is a tool failure: %+v", rep)
	}
}

func TestSetupFilesCreated(t *testing.T) {
	e, m := env(t, goodModel(t))
	Run(context.Background(), e, QuickSuite()[1:2])
	if !strings.Contains(m.f["/workspace/config.txt"], "port=9090") {
		t.Fatalf("%v", m.f)
	}
}

func TestCompareDetectsRegression(t *testing.T) {
	prev := Report{Digest: "a", PassRate: 1, ToolFailRate: 0}
	cur := Report{Digest: "b", PassRate: 0.5, ToolFailRate: 0.3}
	got := strings.Join(Compare(prev, cur), "|")
	for _, w := range []string{"digest changed", "pass rate dropped", "tool-call failure rate"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if len(Compare(prev, prev)) != 0 {
		t.Fatal("false regression")
	}
}

func TestSuiteDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, tk := range Suite() {
		if tk.ID == "" || tk.Prompt == "" || tk.Check == nil || tk.Category == "" || seen[tk.ID] {
			t.Errorf("bad or duplicate task %+v", tk.ID)
		}
		seen[tk.ID] = true
	}
	for _, want := range []string{"go-project", "python-project", "node-project", "c-project", "python-bugfix", "git-workflow", "jq-data"} {
		if !seen[want] {
			t.Errorf("hard suite missing %s", want)
		}
	}
	if len(HardSuite()) != 7 || len(Suite()) != len(QuickSuite())+7 {
		t.Fatal("suite composition")
	}
}
