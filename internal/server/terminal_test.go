// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

func TestTerminalIsolationAndPolicy(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)

	// another user's session looks like it does not exist
	if code, _, _ := other.do("GET", "/api/sessions/"+id+"/terminal", nil); code != 404 {
		t.Fatalf("other user: %d", code)
	}
	// plain GET without upgrade headers is refused, never a shell
	if code, _, _ := u.do("GET", "/api/sessions/"+id+"/terminal", nil); code == 200 {
		t.Fatal("a non-websocket request must not succeed")
	}
	// admin switch
	users, _ := e.st.ListUsers(context.Background())
	for _, x := range users {
		if x.Email == "a@example.com" {
			x.TerminalDisabled = true
			_ = e.st.UpdateUserPolicy(context.Background(), x)
		}
	}
	if code, m, _ := u.do("GET", "/api/sessions/"+id+"/terminal", nil); code != 403 || !strings.Contains(m["error"].(string), "turned the terminal off") {
		t.Fatalf("admin switch: %d %v", code, m)
	}
	if fake.Count() != 0 {
		t.Fatal("a sandbox was created for a refused terminal")
	}
}

func TestTerminalRoundTripAndTakeover(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.user.URL, "http")+"/api/sessions/"+id+"/terminal?rows=30&cols=100", &websocket.DialOptions{HTTPClient: u.hc})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1 := dial()
	if err := c1.Write(ctx, websocket.MessageBinary, []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	_, got, err := c1.Read(ctx)
	if err != nil || string(got) != "echo hi\n" {
		t.Fatalf("%q %v", got, err)
	}
	_ = c1.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":120}`))
	if len(fake.Containers) != 1 {
		t.Fatalf("containers %d", len(fake.Containers))
	}
	for _, c := range fake.Containers {
		if c.Spec.Runtime != "runsc" || len(c.Execs) != 1 || c.Execs[0].WorkingDir != sandbox.WorkspacePath {
			t.Fatalf("terminal must run in the same hardened sandbox: %+v", c)
		}
	}
	// a second connection replaces the first
	c2 := dial()
	defer c2.CloseNow()
	if _, _, err := c1.Read(ctx); err == nil || !strings.Contains(err.Error(), "Opened somewhere else") {
		t.Fatalf("first connection should be closed with a reason: %v", err)
	}
	// starting an agent run closes the terminal
	e.app.Terms.CloseSession(id, "The agent started working, so the terminal was closed.")
	if _, _, err := c2.Read(ctx); err == nil || !strings.Contains(err.Error(), "agent started working") {
		t.Fatalf("%v", err)
	}
}
