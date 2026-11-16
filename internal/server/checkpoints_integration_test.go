// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// A real session in a real gVisor sandbox: take checkpoints around changes, restore the files, and
// rewind the conversation to the same moment.
func TestIntegrationCheckpointsRestoreFilesAndConversation(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	u := e.userSession("cp@example.com")
	uid := userID(t, e, "cp@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	ctx := context.Background()
	sess, _ := e.st.GetChatSession(ctx, id)
	user, _ := e.st.GetUser(ctx, uid)
	cid, err := e.app.ensureSandbox(ctx, sess, user)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.app.Sandbox.Destroy(ctx, id, cid)
		_ = e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid))
	})
	run := func(script string) string {
		r, err := e.app.Sandbox.Exec(ctx, id, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 30 * time.Second})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("%q: %v %+v", script, err, r)
		}
		return strings.TrimSpace(r.Stdout)
	}
	add := func(role, text string) int64 {
		mid, err := e.st.AppendMessage(ctx, store.Message{SessionID: id, Role: role, Content: text, CreatedAt: e.now})
		if err != nil {
			t.Fatal(err)
		}
		return mid
	}

	// version 1 of the work, with a checkpoint and the conversation up to here
	run("echo one > /workspace/a.txt")
	add("user", "make a.txt say one")
	add("assistant", "done")
	mgr, err := e.app.checkpointsFor(ctx, sess, user)
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := mgr.Snapshot(ctx, "after the first change")
	if err != nil {
		t.Fatal(err)
	}
	e.app.markCheckpoint(ctx, uid, id, cp1.Seq, cp1.Label)

	// version 2, further into the conversation
	run("echo two > /workspace/a.txt && echo extra > /workspace/b.txt")
	add("user", "now make it two and add b.txt")
	add("assistant", "done again")

	// the timeline lists the checkpoint and says it can rewind this session's conversation
	code, body, raw := u.do("GET", "/api/sessions/"+id+"/checkpoints", nil)
	if code != 200 || body["available"] != true || !strings.Contains(string(raw), "after the first change") || !strings.Contains(string(raw), `"can_rewind_conversation":true`) {
		t.Fatalf("%d %s", code, raw)
	}
	// what changed since then
	_, d, _ := u.do("GET", "/api/sessions/"+id+"/checkpoints/"+itoa(int64(cp1.Seq))+"/diff", nil)
	if !strings.Contains(d["diff"].(string), "+two") || !strings.Contains(d["diff"].(string), "b.txt") {
		t.Fatalf("%v", d)
	}
	// restoring files alone leaves the conversation
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/checkpoints/"+itoa(int64(cp1.Seq))+"/restore", map[string]any{"conversation": false}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := run("cat /workspace/a.txt; ls /workspace | grep -c .^b.txt$. || true"); !strings.HasPrefix(got, "one") || !strings.HasSuffix(got, "0") {
		t.Fatalf("the files were not put back: %q", got)
	}
	if ms, _ := e.st.ListMessages(ctx, id, false); len(ms) != 4 {
		t.Fatalf("restoring files only must not touch the conversation: %d messages", len(ms))
	}
	// the restore was itself checkpointed, so it can be undone: go back to the later state
	cps, _ := mgr.List(ctx)
	last := cps[len(cps)-1]
	if !strings.Contains(last.Label, "before restore") {
		t.Fatalf("no safety checkpoint: %+v", last)
	}
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/checkpoints/"+itoa(int64(last.Seq))+"/restore", map[string]any{"conversation": false}); code != 200 {
		t.Fatal("undo")
	}
	if got := run("cat /workspace/a.txt"); got != "two" {
		t.Fatalf("the undo did not bring the later state back: %q", got)
	}
	// restoring with the conversation sets the later messages aside
	code, res, raw := u.do("POST", "/api/sessions/"+id+"/checkpoints/"+itoa(int64(cp1.Seq))+"/restore", map[string]any{"conversation": true})
	if code != 200 || res["messages_set_aside"] != float64(2) {
		t.Fatalf("%d %s", code, raw)
	}
	ms, _ := e.st.ListMessages(ctx, id, false)
	if len(ms) != 2 || ms[1].Content != "done" {
		t.Fatalf("the conversation should be back at the checkpoint: %+v", ms)
	}
	if all, _ := e.st.ListMessages(ctx, id, true); len(all) != 4 {
		t.Fatal("set-aside messages must be kept, not deleted")
	}
	// someone else cannot see or restore it
	other := e.userSession("other@example.com")
	if code, _, _ := other.do("POST", "/api/sessions/"+id+"/checkpoints/"+itoa(int64(cp1.Seq))+"/restore", map[string]any{}); code != 404 {
		t.Fatalf("%d", code)
	}
}
