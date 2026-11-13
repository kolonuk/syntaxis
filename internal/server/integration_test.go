// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Full path against real Docker + gVisor: HTTP API -> background run -> real
// sandbox -> commands executed by the server -> output persisted. Skips when
// Docker, runsc or the sandbox image are missing.
func TestIntegrationAgentRunInRealGVisorSandbox(t *testing.T) {
	d, err := sandbox.NewDocker("")
	if err != nil {
		t.Skip("no docker client")
	}
	defer d.Close()
	ctx := context.Background()
	if d.Ping(ctx) != nil {
		t.Skip("docker not reachable")
	}
	cfg := sandbox.DefaultConfig()
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("runsc not registered")
	}
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("sandbox image not built")
	}
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = sandbox.NewManager(d, cfg, nil)
	call := func(id, cmd string) provider.ToolCall {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return provider.ToolCall{ID: id, Name: "run_shell", Arguments: b}
	}
	sp := &stubProvider{replies: []*provider.ChatResult{
		{ToolCalls: []provider.ToolCall{call("c1", "echo built-in-gvisor > out.txt && cat out.txt && id -u && uname -r")}},
		{ToolCalls: []provider.ToolCall{call("c2", "curl -s -m 3 http://192.168.1.1/admin")}},
		{Content: "finished"},
	}}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})

	u := e.userSession("it@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	sess, _ := e.st.GetChatSession(ctx, id)
	t.Cleanup(func() {
		if cur, err := e.st.GetChatSession(ctx, id); err == nil && cur.SandboxID != "" {
			_ = e.app.Sandbox.Destroy(ctx, id, cur.SandboxID)
		}
		_ = d.RemoveVolume(ctx, sandbox.WorkspaceVolume(sess.UserID))
	})
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "do things"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	done := make(chan struct{})
	go func() { e.app.Runs.Get(id).Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("run did not finish")
	}
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/messages", nil)
	out := string(raw)
	for _, want := range []string{"built-in-gvisor", "10001", "gvisor", "blocked by policy", "finished"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("history missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<html") {
		t.Fatal("the LAN request was not blocked")
	}
}
