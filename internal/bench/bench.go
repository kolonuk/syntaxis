// SPDX-License-Identifier: GPL-2.0-or-later

// Package bench is the admin-only model benchmark: a small suite of
// coding-agent tasks run against a model in a throwaway sandbox. It reports
// pass rate, tokens/s, time to first token, tool-call failure rate and context
// behaviour, so changes after a model update are visible.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// Task is one benchmark case. Check inspects the final state via exec and the
// agent's final text.
type Task struct {
	ID       string
	Category string // tool-calling | edit | instruction | multi-step
	Prompt   string
	Setup    map[string]string // files to create before the run
	Check    func(ctx context.Context, exec ExecFunc, final string) (bool, string)
}

// ExecFunc runs a command in the sandbox.
type ExecFunc func(ctx context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error)

func readFile(ctx context.Context, exec ExecFunc, p string) (string, bool) {
	r, err := exec(ctx, sandbox.ExecSpec{Cmd: []string{"cat", "--", p}, Timeout: 20 * time.Second})
	return r.Stdout, err == nil && r.ExitCode == 0
}

// Suite returns the built-in tasks.
func Suite() []Task {
	return []Task{
		{ID: "write-file", Category: "tool-calling",
			Prompt: "Create the file /workspace/hello.txt containing exactly the text: hello syntaxis (no trailing text). Then reply 'done'.",
			Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
				c, ok := readFile(ctx, ex, "/workspace/hello.txt")
				return ok && strings.TrimSpace(c) == "hello syntaxis", "hello.txt content: " + strings.TrimSpace(c)
			}},
		{ID: "edit-line", Category: "edit", Setup: map[string]string{"/workspace/config.txt": "name=old\nport=8080\nmode=dev\n"},
			Prompt: "In /workspace/config.txt change port to 9090 and leave every other line unchanged. Then reply 'done'.",
			Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
				c, ok := readFile(ctx, ex, "/workspace/config.txt")
				return ok && c == "name=old\nport=9090\nmode=dev\n", "config.txt: " + strings.ReplaceAll(c, "\n", "|")
			}},
		{ID: "format-following", Category: "instruction",
			Prompt: "Without running any tools, reply with exactly one line in the form ANSWER: <number> where the number is 17 plus 25.",
			Check: func(_ context.Context, _ ExecFunc, final string) (bool, string) {
				return strings.TrimSpace(final) == "ANSWER: 42", "reply: " + strings.TrimSpace(final)
			}},
		{ID: "multi-step", Category: "multi-step", Setup: map[string]string{"/workspace/data.txt": "3\n1\n2\n"},
			Prompt: "Read /workspace/data.txt, write the numbers sorted ascending, one per line, to /workspace/sorted.txt, then reply with the sum of the numbers as a single integer.",
			Check: func(ctx context.Context, ex ExecFunc, final string) (bool, string) {
				c, ok := readFile(ctx, ex, "/workspace/sorted.txt")
				return ok && strings.TrimSpace(c) == "1\n2\n3" && strings.Contains(final, "6"), "sorted.txt: " + strings.ReplaceAll(strings.TrimSpace(c), "\n", ",")
			}},
	}
}

// TaskResult is one task's outcome.
type TaskResult struct {
	ID        string  `json:"id"`
	Category  string  `json:"category"`
	Passed    bool    `json:"passed"`
	Detail    string  `json:"detail"`
	Turns     int     `json:"turns"`
	ToolCalls int     `json:"tool_calls"`
	ToolFails int     `json:"tool_failures"`
	Status    string  `json:"status"`
	Seconds   float64 `json:"seconds"`
}

// Report is the stored result for one model run.
type Report struct {
	Model         string       `json:"model"`
	Digest        string       `json:"digest"`
	At            time.Time    `json:"at"`
	Tasks         []TaskResult `json:"tasks"`
	PassRate      float64      `json:"pass_rate"`
	TokensPerSec  float64      `json:"tokens_per_sec"`
	TTFTMillis    float64      `json:"ttft_ms"`
	ToolFailRate  float64      `json:"tool_call_failure_rate"`
	Window        int          `json:"context_window"`
	MaxPromptUsed int          `json:"max_prompt_tokens"`
	ContextNote   string       `json:"context_note"`
}

// Env is what a run needs. Chat should go through the provider scheduler at
// the lowest (benchmark) priority.
type Env struct {
	Model  string
	Digest string
	Window int
	Chat   func(ctx context.Context, req provider.ChatRequest, onDelta func(string)) (*provider.ChatResult, error)
	Exec   ExecFunc
	Now    func() time.Time
}

// Run executes the tasks and aggregates metrics.
func Run(ctx context.Context, env Env, tasks []Task) Report {
	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	rep := Report{Model: env.Model, Digest: env.Digest, At: now().UTC(), Window: env.Window}
	var totTok int
	var totSec float64
	var ttfts []float64
	var calls, fails int
	pol, _ := agent.NewApprovalPolicy()
	for _, t := range tasks {
		for p, c := range t.Setup {
			_, _ = env.Exec(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "_", p}, Stdin: []byte(c), Timeout: 20 * time.Second})
		}
		tr := TaskResult{ID: t.ID, Category: t.Category}
		start := now()
		tcalls, tfails := 0, 0
		var final string
		chat := func(c context.Context, req provider.ChatRequest, on func(string)) (*provider.ChatResult, error) {
			res, err := env.Chat(c, req, on)
			if err == nil {
				if u := agent.TotalTokens(req.Messages); u > rep.MaxPromptUsed {
					rep.MaxPromptUsed = u
				}
				if res.Duration > 0 && res.Usage.CompletionTokens > 0 {
					totTok += res.Usage.CompletionTokens
					totSec += res.Duration.Seconds()
				}
				if res.TTFT > 0 {
					ttfts = append(ttfts, float64(res.TTFT.Milliseconds()))
				}
			}
			return res, err
		}
		a := &agent.Agent{
			Cfg: agent.Config{Model: env.Model, Window: env.Window, Autonomy: agent.Full, ToolsEnabled: true, MaxTurns: 12, MaxDuration: 5 * time.Minute,
				System: "You are a coding agent in a sandbox. Workspace: /workspace. Use the tools to do what is asked, then reply briefly."},
			Deps: agent.Deps{Chat: chat, Tools: &agent.Executor{Exec: env.Exec}, Policy: pol,
				Emit: func(e agent.Event) {
					switch e.Kind {
					case agent.EvToolCall:
						tcalls++
					case agent.EvToolResult:
						if s, _ := e.Data["output"].(string); strings.HasPrefix(s, "error:") {
							tfails++
						}
					}
				}},
		}
		res := a.Run(ctx, t.Prompt)
		final = res.Final
		tr.Turns, tr.ToolCalls, tr.ToolFails, tr.Status = res.Turns, tcalls, tfails, res.Status
		tr.Seconds = now().Sub(start).Seconds()
		if res.Status == agent.StatusFinished || res.Status == agent.StatusCapTurns {
			tr.Passed, tr.Detail = t.Check(ctx, env.Exec, final)
		} else {
			tr.Detail = "run ended: " + res.Status + " " + res.Reason
		}
		calls += tcalls
		fails += tfails
		rep.Tasks = append(rep.Tasks, tr)
	}
	pass := 0
	for _, t := range rep.Tasks {
		if t.Passed {
			pass++
		}
	}
	if n := len(rep.Tasks); n > 0 {
		rep.PassRate = float64(pass) / float64(n)
	}
	if totSec > 0 {
		rep.TokensPerSec = float64(totTok) / totSec
	}
	if len(ttfts) > 0 {
		var s float64
		for _, v := range ttfts {
			s += v
		}
		rep.TTFTMillis = s / float64(len(ttfts))
	}
	if calls > 0 {
		rep.ToolFailRate = float64(fails) / float64(calls)
	}
	switch {
	case env.Window > 0 && rep.MaxPromptUsed > env.Window:
		rep.ContextNote = fmt.Sprintf("prompt reached %d tokens, over the %d-token window", rep.MaxPromptUsed, env.Window)
	case env.Window > 0:
		rep.ContextNote = fmt.Sprintf("peak prompt %d of %d tokens", rep.MaxPromptUsed, env.Window)
	}
	return rep
}

// JSON serialises a report for storage.
func (r Report) JSON() string { b, _ := json.Marshal(r); return string(b) }

// Compare describes a regression between two reports of the same model.
func Compare(prev, cur Report) []string {
	var out []string
	if prev.Digest != "" && cur.Digest != "" && prev.Digest != cur.Digest {
		out = append(out, "model digest changed since the last run")
	}
	if cur.PassRate < prev.PassRate {
		out = append(out, fmt.Sprintf("pass rate dropped from %.0f%% to %.0f%%", prev.PassRate*100, cur.PassRate*100))
	}
	if cur.ToolFailRate > prev.ToolFailRate+0.1 {
		out = append(out, "tool-call failure rate increased")
	}
	return out
}
