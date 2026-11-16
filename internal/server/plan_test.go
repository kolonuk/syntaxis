// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
)

// Plan mode with the real model and a real sandbox: the planning step proposes and changes nothing;
// approving (with an edit) carries it out; asking for changes proposes again.
func TestLivePlanModeProposesBeforeChangingAnything(t *testing.T) {
	l := liveEnv(t)
	u := l.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]any{"model": l.model, "autonomy": "full", "plan": true})
	id := s["id"].(string)
	if s["plan_mode"] != true {
		t.Fatalf("%v", s)
	}
	state := func() map[string]any { _, g, _ := u.do("GET", "/api/sessions/"+id, nil); return g }
	l.send(u, id, "Create a file /workspace/hello.txt containing the word hi.")
	got := state()
	if got["state"] != "needs_input" || got["plan_state"] != "proposed" || strings.TrimSpace(got["plan_text"].(string)) == "" {
		t.Fatalf("a proposed plan must wait for the user: %v\n%s", got, l.history(u, id))
	}
	// the planning step changes nothing
	if out := l.sandboxRun(id, "ls /workspace"); strings.Contains(out, "hello.txt") {
		t.Fatalf("a file was created during the planning step: %q", out)
	}
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "bogus"}); code != 400 {
		t.Fatal("a bad decision was accepted")
	}
	// approve with an edit: the work starts, with the edited plan in its instructions
	edited := "1. Create /workspace/hello.txt containing exactly: hi\n2. Reply done."
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "approve", "plan": edited}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	l.wait(id)
	got = state()
	if got["plan_state"] != "approved" || got["state"] != "idle" {
		t.Fatalf("the approved plan should be carried out: %v\n%s", got, l.history(u, id))
	}
	if out := l.sandboxRun(id, "cat /workspace/hello.txt"); !strings.Contains(out, "hi") {
		t.Fatalf("the approved plan was not carried out: %q\n%s", out, l.history(u, id))
	}
	// no plan is waiting any more
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/plan", map[string]any{"action": "approve"}); code != 409 {
		t.Fatalf("%d", code)
	}

	// a second session: ask for changes instead of approving
	_, s2, _ := u.do("POST", "/api/sessions", map[string]any{"model": l.model, "autonomy": "full", "plan": true})
	id2 := s2["id"].(string)
	l.send(u, id2, "Plan how to add a README.md to an empty project.")
	if g := func() map[string]any { _, g, _ := u.do("GET", "/api/sessions/"+id2, nil); return g }(); g["plan_state"] != "proposed" {
		t.Fatalf("%v\n%s", g, l.history(u, id2))
	}
	if code, _, _ := u.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "revise"}); code != 400 {
		t.Fatal("a revision without feedback was accepted")
	}
	if code, _, raw := u.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "revise", "feedback": "also add a LICENSE file"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	l.wait(id2)
	_, g2, _ := u.do("GET", "/api/sessions/"+id2, nil)
	if g2["plan_state"] != "proposed" || !strings.Contains(strings.ToLower(g2["plan_text"].(string)), "licen") {
		t.Fatalf("a revised plan should be proposed again with the change: %v", g2)
	}
	// someone else's session
	other := l.userSession("b@example.com")
	if code, _, _ := other.do("POST", "/api/sessions/"+id2+"/plan", map[string]any{"action": "approve"}); code != 404 {
		t.Fatalf("%d", code)
	}
}

// The diff is read from the session's real git workspace inside its real sandbox.
func TestDiffShowsTheSessionsChangesAndNoOneElses(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]any{"model": "prov/m1"})
	id := s["id"].(string)
	if _, d, _ := u.do("GET", "/api/sessions/"+id+"/diff", nil); d["available"] != false {
		t.Fatalf("before anything ran there is nothing to show: %v", d)
	}
	ctx := context.Background()
	sess, _ := e.st.GetChatSession(ctx, id)
	usr, _ := e.st.GetUser(ctx, sess.UserID)
	cid, err := e.app.ensureSandbox(ctx, sess, usr)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ = e.st.GetChatSession(ctx, id)
	script := "cd /workspace && git init -q && git config user.email t@example.com && git config user.name t && echo one > a.go && git add . && git commit -qm init && echo new-line >> a.go"
	if r, err := e.app.Sandbox.Exec(ctx, id, cid, execSpec(script)); err != nil || r.ExitCode != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	_, d, raw := u.do("GET", "/api/sessions/"+id+"/diff", nil)
	if d["available"] != true || !strings.Contains(d["text"].(string), "+new-line") || !strings.Contains(d["text"].(string), "a.go") {
		t.Fatalf("%s", raw)
	}
	if code, _, _ := other.do("GET", "/api/sessions/"+id+"/diff", nil); code != 404 {
		t.Fatalf("another user must not read the diff: %d", code)
	}
}
