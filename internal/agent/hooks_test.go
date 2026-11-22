// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

func TestParseHooks(t *testing.T) {
	hs, err := ParseHooks(`{"hooks":[{"event":"post_tool","tools":["write_file","patch_file"],"command":" gofmt -l . "},{"event":"pre_tool","command":"test -f .ok","timeout_seconds":5}]}`, "user")
	if err != nil || len(hs) != 2 || hs[0].Command != "gofmt -l ." || hs[0].Source != "user" || hs[1].Timeout != 5 {
		t.Fatalf("%v %+v", err, hs)
	}
	if hs, err := ParseHooks("  ", "user"); err != nil || hs != nil {
		t.Fatal("blank means no hooks")
	}
	many := `{"hooks":[` + strings.Repeat(`{"event":"pre_tool","command":"true"},`, MaxHooks) + `{"event":"pre_tool","command":"true"}]}`
	for name, raw := range map[string]string{
		"not json":      `hooks`,
		"bad event":     `{"hooks":[{"event":"during","command":"true"}]}`,
		"no command":    `{"hooks":[{"event":"pre_tool","command":"  "}]}`,
		"long command":  `{"hooks":[{"event":"pre_tool","command":"` + strings.Repeat("x", 1001) + `"}]}`,
		"unknown tool":  `{"hooks":[{"event":"pre_tool","tools":["rm_rf"],"command":"true"}]}`,
		"plan tool":     `{"hooks":[{"event":"pre_tool","tools":["propose_plan"],"command":"true"}]}`,
		"long timeout":  `{"hooks":[{"event":"pre_tool","command":"true","timeout_seconds":9999}]}`,
		"neg timeout":   `{"hooks":[{"event":"pre_tool","command":"true","timeout_seconds":-1}]}`,
		"too many":      many,
		"event missing": `{"hooks":[{"command":"true"}]}`,
	} {
		if _, err := ParseHooks(raw, "user"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func hookHarness(t *testing.T, hooks string, reply ...*provider.ChatResult) *harness {
	t.Helper()
	h := newHarness(t)
	hs, err := ParseHooks(hooks, "repository")
	if err != nil {
		t.Fatal(err)
	}
	h.a.Cfg.Hooks = hs
	h.model.replies = reply
	return h
}

func TestPostHookOutputIsAddedToTheToolResult(t *testing.T) {
	h := hookHarness(t, `{"hooks":[{"event":"post_tool","tools":["write_file"],"command":"gofmt -l ."}]}`,
		toolReply(call("w1", ToolWrite, `{"path":"a.go","content":"package a\n"}`)), &provider.ChatResult{Content: "done"})
	h.box.run = func(argv []string) (sandbox.ExecResult, bool) {
		if len(argv) == 3 && argv[2] == "gofmt -l ." {
			return sandbox.ExecResult{ExitCode: 1, Stdout: "a.go\n"}, true
		}
		return sandbox.ExecResult{}, false
	}
	r := h.a.Run(context.Background(), "write it")
	if r.Status != StatusFinished {
		t.Fatalf("%+v", r)
	}
	msgs := h.model.requests[1].Messages
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "wrote ") || !strings.Contains(last, "[hook gofmt -l .: exit 1]") || !strings.Contains(last, "a.go") {
		t.Fatalf("the model did not see the hook's output: %q", last)
	}
	// the hook did not run for a tool it was not attached to
	h2 := hookHarness(t, `{"hooks":[{"event":"post_tool","tools":["write_file"],"command":"gofmt -l ."}]}`,
		toolReply(call("r1", ToolRead, `{"path":"a.go"}`)), &provider.ChatResult{Content: "done"})
	h2.box.files["/workspace/a.go"] = "x"
	h2.a.Run(context.Background(), "read it")
	if strings.Contains(strings.Join(h2.box.cmds, "\n"), "gofmt") {
		t.Fatal("a hook ran for a tool it is not attached to")
	}
}

func TestPreHookCanRefuseAToolCall(t *testing.T) {
	h := hookHarness(t, `{"hooks":[{"event":"pre_tool","tools":["write_file","patch_file"],"command":"echo protected path >&2; exit 3"}]}`,
		toolReply(call("w1", ToolWrite, `{"path":"a.go","content":"package a\n"}`)), &provider.ChatResult{Content: "ok"})
	h.box.run = func(argv []string) (sandbox.ExecResult, bool) {
		if len(argv) == 3 && strings.HasPrefix(argv[2], "echo protected") {
			return sandbox.ExecResult{ExitCode: 3, Stderr: "protected path\n"}, true
		}
		return sandbox.ExecResult{}, false
	}
	h.a.Run(context.Background(), "write it")
	if _, written := h.box.files["/workspace/a.go"]; written {
		t.Fatal("the tool ran although a pre hook refused it")
	}
	last := h.model.requests[1].Messages
	if c := last[len(last)-1].Content; !strings.Contains(c, "error: refused by a hook") || !strings.Contains(c, "protected path") {
		t.Fatalf("%q", c)
	}
	// a passing pre hook lets the call through
	h = hookHarness(t, `{"hooks":[{"event":"pre_tool","command":"true"}]}`, toolReply(call("w1", ToolWrite, `{"path":"a.go","content":"x"}`)), &provider.ChatResult{Content: "ok"})
	h.a.Run(context.Background(), "write it")
	if h.box.files["/workspace/a.go"] != "x" {
		t.Fatal("a passing hook blocked the call")
	}
}

// A hook never asks the user, so one that the approval policy would stop or ask about is not run:
// a repository's hooks file cannot do what the agent itself would have to ask about.
func TestHooksThatNeedApprovalOrAreBlockedAreNotRun(t *testing.T) {
	for _, cmd := range []string{"git push --force origin main", "curl http://169.254.169.254/latest/meta-data/", "terraform apply -auto-approve", "nmap 10.0.0.0/24"} {
		h := hookHarness(t, `{"hooks":[{"event":"post_tool","command":"`+cmd+`"},{"event":"pre_tool","command":"`+cmd+`"}]}`,
			toolReply(call("s1", ToolShell, `{"command":"echo hi"}`)), &provider.ChatResult{Content: "ok"})
		h.a.Run(context.Background(), "go")
		for _, c := range h.box.cmds {
			if strings.Contains(c, cmd) {
				t.Errorf("a hook with %q was run", cmd)
			}
		}
		if !h.hasEvent(EvNotice) || !strings.Contains(strings.Join(h.audits, "\n"), "agent.hook.skipped") {
			t.Errorf("%q: the user is not told and the skip is not audited", cmd)
		}
	}
}

func TestHooksDoNotRunWhilePlanningAndSeeTheToolInEnvironment(t *testing.T) {
	h := hookHarness(t, `{"hooks":[{"event":"pre_tool","command":"true"}]}`, toolReply(call("l1", ToolList, `{}`)), &provider.ChatResult{Content: "ok"})
	h.a.Cfg.PlanPhase = true
	h.a.Run(context.Background(), "go")
	for _, c := range h.box.cmds {
		if c == "sh -c true" {
			t.Fatal("a hook ran while planning")
		}
	}
	h = hookHarness(t, `{"hooks":[{"event":"pre_tool","command":"true"}]}`, toolReply(call("w1", ToolWrite, `{"path":"a","content":"b"}`)), &provider.ChatResult{Content: "ok"})
	var env []string
	h.a.Deps.Tools.Exec = func(c context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
		if len(e.Cmd) == 3 && e.Cmd[2] == "true" {
			env = e.Env
		}
		return h.box.exec(c, e)
	}
	h.a.Run(context.Background(), "go")
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "SYNTAXIS_TOOL=write_file") || !strings.Contains(joined, "SYNTAXIS_HOOK=pre_tool") || !strings.Contains(joined, "SYNTAXIS_TOOL_INPUT=") {
		t.Fatalf("%v", env)
	}
}

// Hooks together get a time budget per run, so a slow hook cannot make every tool call slow.
func TestHooksStopWhenTheirTimeBudgetIsSpent(t *testing.T) {
	h := hookHarness(t, `{"hooks":[{"event":"pre_tool","command":"true"}]}`,
		toolReply(call("w1", ToolWrite, `{"path":"a","content":"1"}`)), toolReply(call("w2", ToolWrite, `{"path":"b","content":"2"}`)),
		toolReply(call("w3", ToolWrite, `{"path":"c","content":"3"}`)), &provider.ChatResult{Content: "ok"})
	now := int64(1000)
	h.a.Deps.Now = func() time.Time { now += 10; return time.Unix(now, 0) } // every reading is 10 s later
	h.a.Cfg.HookBudget = time.Second
	var told time.Duration
	h.a.Deps.HookTime = func(d time.Duration) { told += d }
	h.a.Run(context.Background(), "go")
	ran := 0
	for _, c := range h.box.cmds {
		if c == "sh -c true" {
			ran++
		}
	}
	if ran != 1 {
		t.Fatalf("the hook ran %d times; after the budget it should not run", ran)
	}
	if told <= 0 {
		t.Error("the time the hook used was not reported")
	}
	if !strings.Contains(strings.Join(h.audits, "\n"), "agent.hook.budget") || !h.hasEvent(EvNotice) {
		t.Error("the user is not told and the budget stop is not audited")
	}
	if h.box.files["/workspace/c"] != "3" {
		t.Error("tool calls must still work once hooks have stopped")
	}
}
