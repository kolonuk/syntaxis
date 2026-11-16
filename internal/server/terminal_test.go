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
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
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
	cs, _ := e.app.Sandbox.RT.List(context.Background())
	for _, c := range cs {
		if c.Labels[sandbox.LabelSession] == id {
			t.Fatal("a sandbox was created for a refused terminal")
		}
	}
}

func TestTerminalRoundTripAndTakeover(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.user.URL, "http")+"/api/sessions/"+id+"/terminal?rows=30&cols=100", &websocket.DialOptions{HTTPClient: u.hc})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1 := dial()
	// a real shell in the real sandbox: the arithmetic is done by the shell, not by an echo
	if err := c1.Write(ctx, websocket.MessageBinary, []byte("echo $((20+22)); pwd; id -u\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !(strings.Contains(out.String(), "\n42") && strings.Contains(out.String(), "/workspace") && strings.Contains(out.String(), "10001")) {
		_, got, err := c1.Read(ctx)
		if err != nil {
			t.Fatalf("the shell did not answer: %v (got %q)", err, out.String())
		}
		out.Write(got)
	}
	_ = c1.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":120}`))
	sess, _ := e.st.GetChatSession(context.Background(), id)
	if got := dockerInspect(t, sess.SandboxID, "{{.HostConfig.Runtime}}"); got != "runsc" {
		t.Fatalf("the terminal must run in the same hardened sandbox, got runtime %q", got)
	}
	// a second connection replaces the first
	c2 := dial()
	defer c2.CloseNow()
	for {
		if _, _, err := c1.Read(ctx); err != nil {
			if !strings.Contains(err.Error(), "Opened somewhere else") {
				t.Fatalf("first connection should be closed with a reason: %v", err)
			}
			break
		}
	}
	// starting an agent run closes the terminal
	e.app.Terms.CloseSession(id, "The agent started working, so the terminal was closed.")
	for {
		if _, _, err := c2.Read(ctx); err != nil {
			if !strings.Contains(err.Error(), "agent started working") {
				t.Fatalf("%v", err)
			}
			return
		}
	}
}

// An open terminal outlives the HTTP request that opened it, so it must notice when
// that sign-in stops being valid (sign out everywhere, password change, disabled account).
func TestTerminalClosesWhenTheSignInIsRevoked(t *testing.T) {
	e := newEnv(t)
	e.app.TerminalRecheck = 50 * time.Millisecond
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.user.URL, "http")+"/api/sessions/"+id+"/terminal", &websocket.DialOptions{HTTPClient: u.hc})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if code, _, _ := u.do("POST", "/api/logout-all", nil); code != 200 {
		t.Fatalf("logout-all: %d", code)
	}
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if !strings.Contains(err.Error(), "Signed out") {
				t.Fatalf("terminal should close with a reason after sign-out: %v", err)
			}
			return
		}
	}
}
