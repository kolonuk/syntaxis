// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func TestPlanModeProposesBeforeChangingAnything(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	var shellRuns int
	fake.ExecHook = func(c *sandbox.FakeContainer, ex sandbox.ExecSpec) (sandbox.ExecResult, bool) {
		if len(ex.Cmd) > 2 && ex.Cmd[2] == "make build" {
			shellRuns++
		}
		if ex.Cmd[0] == "git" || strings.Contains(strings.Join(ex.Cmd, " "), "head -c") || strings.Contains(strings.Join(ex.Cmd, " "), "[ -d") {
			return sandbox.ExecResult{ExitCode: 1}, true
		}
		return sandbox.ExecResult{}, false
	}
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	call := func(id, name, args string) provider.ToolCall {
		return provider.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
	}
	sp := &stubProvider{replies: []*provider.ChatResult{
		// planning step: it tries to run a command (refused), then proposes
		{ToolCalls: []provider.ToolCall{call("c1", "run_shell", `{"command":"make build"}`)}},
		{ToolCalls: []provider.ToolCall{call("c2", "propose_plan", `{"plan":"1. Add the endpoint\n2. Test it"}`)}},
		// after approval: it does the work
		{ToolCalls: []provider.ToolCall{call("c3", "run_shell", `{"command":"make build"}`)}},
		{Content: "Built."},
		// after a request for changes: a revised plan
		{ToolCalls: []provider.ToolCall{call("c4", "propose_plan", `{"plan":"1. Add the endpoint with pagination"}`)}},
	}}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")

	wait := func(id string) map[string]any {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, got, _ := u.do("GET", "/api/sessions/"+id, nil); got["state"] != "running" {
				return got
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("run did not finish")
		return nil
	}
	_, s, _ := u.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "autonomy": "full", "plan": true})
	id := s["id"].(string)
	if s["plan_mode"] != true {
		t.Fatalf("%v", s)
	}
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "add an endpoint"})
	got := wait(id)
	if got["state"] != "needs_input" || got["plan_state"] != "proposed" || !strings.Contains(got["plan_text"].(string), "Add the endpoint") {
		t.Fatalf("a proposed plan must wait for the user: %v", got)
	}
	if shellRuns != 0 {
		t.Fatal("a command ran during the planning step")
	}
	// the model was offered only the planning tools
	for _, d := range sp.reqs[0].Tools {
		if d.Name == "run_shell" || d.Name == "write_file" || d.Name == "patch_file" {
			t.Fatalf("%s offered while planning", d.Name)
		}
	}
	// nothing can be done until the plan is decided
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "bogus"}); code != 400 {
		t.Fatal("a bad decision was accepted")
	}
	// approve with an edit: the work starts and the edited plan is in the instructions
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "approve", "plan": "1. Add the endpoint\n2. Test it\n3. Update the docs"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	got = wait(id)
	if got["plan_state"] != "approved" || got["state"] != "idle" || shellRuns != 1 {
		t.Fatalf("the approved plan should be carried out: %v (shell runs %d)", got, shellRuns)
	}
	last := sp.reqs[len(sp.reqs)-1]
	if sys := last.Messages[0].Content; !strings.Contains(sys, "<approved_plan>") || !strings.Contains(sys, "Update the docs") || strings.Contains(sys, "Planning step") {
		t.Fatal("the execution step must carry the approved plan and not the planning rules")
	}
	hasShell := false
	for _, d := range sp.reqs[2].Tools {
		if d.Name == "run_shell" {
			hasShell = true
		}
	}
	if !hasShell {
		t.Fatal("the full tools must be available after approval")
	}
	// no plan is waiting any more
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "approve"}); code != 409 {
		t.Fatalf("%d", code)
	}

	// a second session: ask for changes instead of approving
	_, s2, _ := u.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "autonomy": "full", "plan": true})
	id2 := s2["id"].(string)
	sp.replies = append([]*provider.ChatResult{{ToolCalls: []provider.ToolCall{call("d1", "propose_plan", `{"plan":"1. First idea"}`)}}, {ToolCalls: []provider.ToolCall{call("d2", "propose_plan", `{"plan":"1. Better idea with pagination"}`)}}}, sp.replies[len(sp.replies):]...)
	sp.reqs = nil
	u.do("POST", "/api/sessions/"+id2+"/messages", map[string]string{"text": "task"})
	wait(id2)
	if code, _, _ := u.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "revise"}); code != 400 {
		t.Fatal("a revision without feedback was accepted")
	}
	if code, _, raw := u.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "revise", "feedback": "add pagination"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	got = wait(id2)
	if got["plan_state"] != "proposed" || !strings.Contains(got["plan_text"].(string), "pagination") {
		t.Fatalf("a revised plan should be proposed again: %v", got)
	}
	// someone else's session
	other := e.userSession("b@example.com")
	if code, _, _ := other.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "approve"}); code != 404 {
		t.Fatalf("%d", code)
	}
}

func TestDiffShowsTheSessionsChangesAndNoOneElses(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	fake.ExecHook = func(c *sandbox.FakeContainer, ex sandbox.ExecSpec) (sandbox.ExecResult, bool) {
		if len(ex.Cmd) > 2 && strings.Contains(ex.Cmd[2], "git diff") {
			if ex.Cmd[3] != "_" || ex.Cmd[4] != "/workspace" {
				t.Errorf("unexpected arguments %v", ex.Cmd)
			}
			return sandbox.ExecResult{Stdout: "### branch syntaxis/x\n### status\n M a.go\n### diff\ndiff --git a/a.go b/a.go\n+new line\n"}, true
		}
		if ex.Cmd[0] == "git" || strings.Contains(strings.Join(ex.Cmd, " "), "head -c") || strings.Contains(strings.Join(ex.Cmd, " "), "[ -d") {
			return sandbox.ExecResult{ExitCode: 1}, true
		}
		return sandbox.ExecResult{}, false
	}
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return &stubProvider{}, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]any{"model": "prov/m1"})
	id := s["id"].(string)
	if _, d, _ := u.do("GET", "/api/sessions/"+id+"/diff", nil); d["available"] != false {
		t.Fatalf("before anything ran there is nothing to show: %v", d)
	}
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "go"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, g, _ := u.do("GET", "/api/sessions/"+id, nil); g["state"] != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, d, raw := u.do("GET", "/api/sessions/"+id+"/diff", nil)
	if d["available"] != true || !strings.Contains(d["text"].(string), "+new line") {
		t.Fatalf("%s", raw)
	}
	if code, _, _ := other.do("GET", "/api/sessions/"+id+"/diff", nil); code != 404 {
		t.Fatalf("another user must not read the diff: %d", code)
	}
}
