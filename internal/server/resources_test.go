// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// A real workspace over its limit in a real sandbox, measured by the real monitor.
func TestFullWorkspaceBlocksNewWorkUntilSpaceIsFreed(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	ctx := context.Background()
	sess, _ := e.st.GetChatSession(ctx, id)
	usr, _ := e.st.GetUser(ctx, sess.UserID)
	cid, err := e.app.ensureSandbox(ctx, sess, usr)
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) {
		t.Helper()
		if r, err := e.app.Sandbox.Exec(ctx, id, cid, execSpec(script)); err != nil || r.ExitCode != 0 {
			t.Fatalf("%q: %v %+v", script, err, r)
		}
	}
	// 6 MiB in a workspace limited to 5 MiB: over the limit, but under the 125% that stops a runaway fill
	run("dd if=/dev/zero of=/workspace/big bs=1M count=6 2>/dev/null")
	cfg := sandbox.DefaultWatchdogConfig()
	cfg.DiskBytesLimit = 5 << 20
	d := e.app.Sandbox.RT.(*sandbox.Docker)
	// the monitor looks at every sandbox its runtime lists, with this test's 5 MiB limit: it must see only this user's
	// sandbox, never another Syntaxis' on the same Docker host (a 6 MiB workspace there would be stopped)
	mon := sandbox.NewMonitor(onlyUser{Runtime: d, user: itoa(usr.ID)}, sandbox.NewWatchdog(cfg), 2, nil)
	mon.DiskBytes = func(c context.Context, cid string) (int64, error) { return sandbox.WorkspaceBytes(c, d, cid) }
	mon.DiskEvery = time.Millisecond
	e.app.Monitor = mon
	mon.Tick(ctx, time.Now())
	time.Sleep(10 * time.Millisecond)
	mon.Tick(ctx, time.Now())

	code, m, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hello"})
	if code != 507 || !strings.Contains(m["error"].(string), "workspace is full") {
		t.Fatalf("a full workspace must refuse new work with an explanation: %d %v", code, m)
	}
	// the terminal is the way out: it must not be blocked by the same check
	if code, body, _ := u.do("GET", "/api/sessions/"+id+"/terminal", nil); code == 507 {
		t.Fatalf("the terminal must stay available to free space: %v", body)
	}
	// the user deletes files; the next message re-measures and goes through at once
	run("rm /workspace/big")
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hello"}); code != 202 {
		t.Fatalf("after freeing space: %d %s", code, raw)
	}
}

// onlyUser is the real Docker engine, but listing only one user's sandboxes.
type onlyUser struct {
	sandbox.Runtime
	user string
}

func (o onlyUser) List(ctx context.Context) ([]sandbox.ContainerInfo, error) {
	all, err := o.Runtime.List(ctx)
	var out []sandbox.ContainerInfo
	for _, c := range all {
		if c.Labels[sandbox.LabelUser] == o.user {
			out = append(out, c)
		}
	}
	return out, err
}
