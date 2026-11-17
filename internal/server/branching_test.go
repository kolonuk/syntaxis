// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

type convo struct {
	e    *env
	u    *client
	id   string
	ids  []int64
	sent chan string
}

// newConvo makes a session with a short conversation in it: two exchanges, the second with a tool call.
func newConvo(t *testing.T, email string) *convo {
	t.Helper()
	e := newEnv(t)
	seedModel(e)
	c := &convo{e: e, sent: make(chan string, 4)}
	e.app.RunAgent = func(_ context.Context, _ *Run, _ *store.ChatSession, _ *store.User, msg string) { c.sent <- msg }
	c.u = e.userSession(email)
	_, s, _ := c.u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "Fix login"})
	c.id = s["id"].(string)
	add := func(role, text, calls, callID string) {
		id, err := e.st.AppendMessage(context.Background(), store.Message{SessionID: c.id, Role: role, Content: text, ToolCalls: calls, ToolCallID: callID, CreatedAt: e.now})
		if err != nil {
			t.Fatal(err)
		}
		c.ids = append(c.ids, id)
	}
	add("user", "first question", "", "")                                                                                 // 0
	add("assistant", "first answer", "", "")                                                                              // 1
	add("user", "second question"+"\n\n<attachments>\n"+agent.AttachmentDir+"/a.png | image | 5\n</attachments>", "", "") // 2
	add("assistant", "", `[{"id":"c1","name":"run_shell","arguments":{}}]`, "")                                           // 3
	add("tool", "output", "", "c1")                                                                                       // 4
	add("assistant", "second answer", "", "")                                                                             // 5
	return c
}

func (c *convo) messages(abandoned bool) []string {
	ms, _ := c.e.st.ListMessages(context.Background(), c.id, abandoned)
	var out []string
	for _, m := range ms {
		out = append(out, m.Role+":"+firstLineOf(m.Content, 30))
	}
	return out
}

func TestEditSetsLaterMessagesAsideAndResends(t *testing.T) {
	c := newConvo(t, "ed@example.com")
	base := "/api/sessions/" + c.id
	// edit the second question: the answer to it, its tool call and its result are set aside
	code, _, raw := c.u.do("POST", base+"/edit", map[string]any{"message_id": c.ids[2], "text": "second question, reworded"})
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	select {
	case msg := <-c.sent:
		text, items := agent.ParseAttachments(msg)
		if text != "second question, reworded" || len(items) != 1 || items[0].Path != agent.AttachmentDir+"/a.png" {
			t.Fatalf("the new text should keep the attachments: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the agent was not started")
	}
	if got := strings.Join(c.messages(false), "|"); got != "user:first question|assistant:first answer" {
		t.Fatalf("%s", got)
	}
	// the set-aside messages are still in the history
	if n := len(c.messages(true)); n != 6 {
		t.Fatalf("%d messages kept", n)
	}
	// editing the very first message rewinds everything
	time.Sleep(50 * time.Millisecond)
	if code, _, _ := c.u.do("POST", base+"/edit", map[string]any{"message_id": c.ids[0], "text": "a different first question"}); code != 202 {
		t.Fatal(code)
	}
	<-c.sent
	if got := c.messages(false); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestEditIsRefusedWhenItCannotWork(t *testing.T) {
	c := newConvo(t, "ed2@example.com")
	base := "/api/sessions/" + c.id
	other := c.e.userSession("ed3@example.com")
	for name, tc := range map[string]struct {
		who  *client
		body map[string]any
		want int
	}{
		"not a user message": {c.u, map[string]any{"message_id": c.ids[1], "text": "x"}, 400},
		"unknown message":    {c.u, map[string]any{"message_id": 99999, "text": "x"}, 400},
		"empty text":         {c.u, map[string]any{"message_id": c.ids[0], "text": "  "}, 400},
		"another user":       {other, map[string]any{"message_id": c.ids[0], "text": "x"}, 404},
	} {
		if code, _, _ := tc.who.do("POST", base+"/edit", tc.body); code != tc.want {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _, _ := c.e.client(c.e.user.URL).do("POST", base+"/edit", map[string]any{"message_id": c.ids[0], "text": "x"}); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	if n := len(c.messages(false)); n != 6 {
		t.Fatalf("a refused edit changed the conversation: %d", n)
	}
	// not while the agent is working
	release := make(chan struct{})
	c.e.app.Runs.Start(c.id, func(context.Context, *Run) { <-release })
	time.Sleep(50 * time.Millisecond)
	if code, _, _ := c.u.do("POST", base+"/edit", map[string]any{"message_id": c.ids[0], "text": "x"}); code != 409 {
		t.Errorf("edit during a run: %d", code)
	}
	if n := len(c.messages(false)); n != 6 {
		t.Fatalf("an edit during a run changed the conversation: %d", n)
	}
	close(release)
}

func TestBranchCopiesTheConversationUpToAReply(t *testing.T) {
	c := newConvo(t, "fk@example.com")
	base := "/api/sessions/" + c.id
	code, s, raw := c.u.do("POST", base+"/fork", map[string]any{"message_id": c.ids[1]})
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	nid := s["id"].(string)
	if nid == c.id || s["title"] != "Fix login (branch)" || s["model"] != "m1" {
		t.Fatalf("%v", s)
	}
	copied, _ := c.e.st.ListMessages(context.Background(), nid, false)
	if len(copied) != 2 || copied[1].Content != "first answer" || copied[0].SessionID != nid {
		t.Fatalf("%+v", copied)
	}
	// the original is untouched
	if n := len(c.messages(false)); n != 6 {
		t.Fatalf("%d", n)
	}
	// with no message, everything is copied; the last reply is a good place
	_, s, _ = c.u.do("POST", base+"/fork", map[string]any{})
	if all, _ := c.e.st.ListMessages(context.Background(), s["id"].(string), false); len(all) != 6 {
		t.Fatalf("%d", len(all))
	}
	// not in the middle of a tool call
	for _, mid := range []int64{c.ids[3], c.ids[4]} {
		if code, _, _ := c.u.do("POST", base+"/fork", map[string]any{"message_id": mid}); code != 400 {
			t.Errorf("forked inside a tool call at %d: %d", mid, code)
		}
	}
	if code, _, _ := c.u.do("POST", base+"/fork", map[string]any{"message_id": 99999}); code != 400 {
		t.Fatal("unknown message")
	}
	other := c.e.userSession("fk2@example.com")
	if code, _, _ := other.do("POST", base+"/fork", map[string]any{}); code != 404 {
		t.Fatalf("another user branched this session: %d", code)
	}
	// messages that were set aside are not copied
	_, _ = c.e.st.AbandonMessagesAfter(context.Background(), c.id, c.ids[1])
	_, s, _ = c.u.do("POST", base+"/fork", map[string]any{})
	if all, _ := c.e.st.ListMessages(context.Background(), s["id"].(string), false); len(all) != 2 {
		t.Fatalf("set-aside messages were copied: %d", len(all))
	}
}

func TestBranchOfAnEmptySessionIsRefused(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("fk3@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	if code, _, _ := u.do("POST", "/api/sessions/"+s["id"].(string)+"/fork", map[string]any{}); code != 400 {
		t.Fatal(code)
	}
}

// A branched session on a repository gets its own working branch, started at the old one.
func TestIntegrationBranchOfARepositorySessionStartsWhereTheOldBranchIs(t *testing.T) {
	c := newConvo(t, "fk4@example.com")
	c.e.app.Sandbox = realSandbox(t, c.e)
	ctx := context.Background()
	uid := userID(t, c.e, "fk4@example.com")
	orig, _ := c.e.st.GetChatSession(ctx, c.id)
	rs := *orig
	rs.ID, rs.RepoKind, rs.RepoName, rs.RepoURL, rs.RepoBase, rs.RepoBranch = newID(), "github", "acme/widgets", "https://example.com/acme/widgets.git", "main", "work-one"
	if err := c.e.st.CreateChatSession(ctx, rs); err != nil {
		t.Fatal(err)
	}
	c.id = rs.ID
	for _, m := range []store.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}} {
		m.SessionID, m.CreatedAt = c.id, c.e.now
		if _, err := c.e.st.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	s := &rs
	user, _ := c.e.st.GetUser(ctx, uid)
	cid, err := c.e.app.ensureSandbox(ctx, s, user)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.e.app.Sandbox.Destroy(ctx, c.id, cid)
		_ = c.e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid))
	})
	sh := func(script string) string {
		r, err := c.e.app.Sandbox.Exec(ctx, c.id, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", script}, WorkingDir: "/", Timeout: 30 * time.Second})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("%q: %v %+v", script, err, r)
		}
		return strings.TrimSpace(r.Stdout)
	}
	sh(`mkdir -p /workspace/widgets && cd /workspace/widgets && git init -q -b work-one && git config user.email t@example.com && git config user.name t && echo one > a.txt && git add . && git commit -qm first && echo two > a.txt && git commit -qam second`)
	tip := sh(`git -C /workspace/widgets rev-parse work-one`)

	code, fork, raw := c.u.do("POST", "/api/sessions/"+c.id+"/fork", map[string]any{})
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	branch, _ := fork["repo_branch"].(string)
	if branch == "" || branch == "work-one" || fork["repo"] != "acme/widgets" {
		t.Fatalf("%v", fork)
	}
	if got := sh(`git -C /workspace/widgets rev-parse ` + branch); got != tip {
		t.Fatalf("the new branch should start at the old one's latest commit: %s vs %s", got, tip)
	}
}
