// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Manager behaviour on a real Docker engine with real gVisor containers. User ids are far above any
// real user's, and the reaper and kill switch only see this test's containers (scoped below).

// scoped is the real Docker engine, but listing only the containers of the given users, so whole-host
// operations (reap, kill all) cannot touch another Syntaxis instance running on the same Docker.
type scoped struct {
	Runtime
	users map[string]bool
}

func (s scoped) List(ctx context.Context) ([]ContainerInfo, error) {
	all, err := s.Runtime.List(ctx)
	var out []ContainerInfo
	for _, c := range all {
		if s.users[c.Labels[LabelUser]] {
			out = append(out, c)
		}
	}
	return out, err
}

func scopedManager(t *testing.T, uids ...string) (*Manager, *Docker) {
	t.Helper()
	m, d := integrationManager(t)
	u := map[string]bool{}
	for _, x := range uids {
		u[x] = true
	}
	m.RT = scoped{Runtime: d, users: u}
	return m, d
}

func cleanup(t *testing.T, m *Manager, d *Docker, sessions map[string]string, uids ...int64) {
	t.Helper()
	t.Cleanup(func() {
		for s, id := range sessions {
			_ = m.Destroy(context.Background(), s, id)
		}
		for _, u := range uids {
			_ = d.RemoveVolume(context.Background(), WorkspaceVolume(u))
		}
	})
}

func TestIntegrationCreateRefusesWithoutRunsc(t *testing.T) {
	m, d := integrationManager(t)
	m.Cfg.Runtime = "syntaxis-no-such-runtime"
	if _, err := m.Create(context.Background(), 910101, "it-norunsc", nil, false); !errors.Is(err, ErrNoRuntime) {
		t.Fatalf("want ErrNoRuntime, got %v", err)
	}
	cs, _ := d.List(context.Background())
	for _, c := range cs {
		if c.Labels[LabelSession] == "it-norunsc" {
			t.Fatal("a container was created without isolation")
		}
	}
}

func TestIntegrationCreateMissingImage(t *testing.T) {
	m, _ := integrationManager(t)
	m.Cfg.Image = "syntaxis-no-such-image:none"
	if _, err := m.Create(context.Background(), 910102, "it-noimage", nil, false); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("%v", err)
	}
}

func TestIntegrationCreateStartsContainerWithItsWorkspace(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uid = 910103
	id, err := m.Create(ctx, uid, "it-create", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, m, d, map[string]string{"it-create": id}, uid)
	ci, err := d.Inspect(ctx, id)
	if err != nil || !ci.Running || ci.Labels[LabelCreated] == "" {
		t.Fatalf("%+v %v", ci, err)
	}
	if r, err := m.Exec(ctx, "it-create", id, ExecSpec{Cmd: []string{"sh", "-c", "pwd; id -u; touch /workspace/x && echo writable"}}); err != nil || r.Stdout != "/workspace\n10001\nwritable\n" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestIntegrationWorkspacesAreIsolatedBetweenUsers(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	a, err := m.Create(ctx, 910104, "it-iso-a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, 910105, "it-iso-b", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, m, d, map[string]string{"it-iso-a": a, "it-iso-b": b}, 910104, 910105)
	run := func(s, id, script string) ExecResult {
		r, err := m.Exec(ctx, s, id, ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run("it-iso-a", a, "echo alice-data > /workspace/secret.txt && cat /workspace/secret.txt"); r.Stdout != "alice-data\n" {
		t.Fatalf("own data: %+v", r)
	}
	if r := run("it-iso-b", b, "cat /workspace/secret.txt; cat /workspace/../syntaxis-ws-u910104/secret.txt"); strings.Contains(r.Stdout, "alice") {
		t.Fatalf("user B read user A's workspace: %+v", r)
	}
	if r := run("it-iso-b", b, "echo x > /etc/syntaxis-probe"); r.ExitCode == 0 {
		t.Fatal("wrote outside the workspace")
	}
}

func TestIntegrationExecLimitsAreReal(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uid = 910106
	id, err := m.Create(ctx, uid, "it-exec", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, m, d, map[string]string{"it-exec": id}, uid)
	if _, err := m.Exec(ctx, "it-exec", id, ExecSpec{Cmd: []string{"sleep", "60"}, Timeout: 2 * time.Second}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a command past its timeout: %v", err)
	}
	r, err := m.Exec(ctx, "it-exec", id, ExecSpec{Cmd: []string{"sh", "-c", "head -c 3000000 /dev/zero | tr '\\0' a"}, Timeout: 20 * time.Second, MaxOutput: 1000})
	if err != nil || len(r.Stdout) > 2000 {
		t.Fatalf("output was not capped: %v %d bytes", err, len(r.Stdout))
	}
}

func TestIntegrationLifecycleStopResumeDestroy(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uid = 910107
	id, err := m.Create(ctx, uid, "it-life", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, m, d, map[string]string{"it-life": id}, uid)
	if err := m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ci, _ := d.Inspect(ctx, id); ci.Running {
		t.Fatal("not stopped")
	}
	if err := m.Resume(ctx, "it-life", id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ci, _ := d.Inspect(ctx, id); !ci.Running {
		t.Fatal("not running after resume")
	}
	if err := d.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume(ctx, "it-life", id); err != nil {
		t.Fatal(err)
	}
	if ci, _ := d.Inspect(ctx, id); ci.Paused {
		t.Fatal("still paused after resume")
	}
	if _, err := m.Exec(ctx, "it-life", id, ExecSpec{Cmd: []string{"sh", "-c", "echo keep > /workspace/kept"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(ctx, "it-life", id); err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(ctx, "it-life", id); err != nil {
		t.Fatalf("destroying a missing container should be fine: %v", err)
	}
	if err := m.Resume(ctx, "it-life", id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume of a missing container: %v", err)
	}
	// the workspace outlives the container
	id2, err := m.Create(ctx, uid, "it-life2", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, m, d, map[string]string{"it-life2": id2})
	if r, _ := m.Exec(ctx, "it-life2", id2, ExecSpec{Cmd: []string{"cat", "/workspace/kept"}}); r.Stdout != "keep\n" {
		t.Fatalf("the workspace did not survive the container: %+v", r)
	}
}

func TestIntegrationReaper(t *testing.T) {
	m, d := scopedManager(t, "910110", "910111", "910112", "910113")
	ctx := context.Background()
	sessions := map[string]string{}
	mk := func(name string, uid int64) string {
		id, err := m.Create(ctx, uid, name, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		sessions[name] = id
		return id
	}
	cleanup(t, m, d, sessions, 910110, 910111, 910112, 910113)
	mk("it-reap-old", 910110)
	time.Sleep(5 * time.Second)
	mk("it-reap-idle", 910111)
	time.Sleep(3 * time.Second)
	mk("it-reap-fresh", 910112)
	mk("it-reap-orphan", 910113)
	// as after a restart: only creation times are known
	m.mu.Lock()
	m.activity = map[string]time.Time{}
	m.mu.Unlock()
	known := map[string]bool{"it-reap-old": true, "it-reap-idle": true, "it-reap-fresh": true}
	res, err := m.Reap(ctx, ReapPolicy{IdleAfter: 2 * time.Second, DestroyAfter: 7 * time.Second, Known: func(s string) bool { return known[s] }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orphans) != 1 || len(res.Destroyed) != 1 || len(res.Stopped) != 1 {
		t.Fatalf("%+v", res)
	}
	state := func(name string) (bool, bool) {
		ci, err := d.Inspect(ctx, sessions[name])
		return err == nil, err == nil && ci.Running
	}
	if there, _ := state("it-reap-orphan"); there {
		t.Fatal("orphan survived")
	}
	if there, _ := state("it-reap-old"); there {
		t.Fatal("expired container survived")
	}
	if there, running := state("it-reap-idle"); !there || running {
		t.Fatal("the idle container should be stopped, not removed")
	}
	if _, running := state("it-reap-fresh"); !running {
		t.Fatal("a fresh container was wrongly stopped")
	}
}

func TestIntegrationKillSwitch(t *testing.T) {
	m, d := scopedManager(t, "910120", "910121")
	ctx := context.Background()
	sessions := map[string]string{}
	cleanup(t, m, d, sessions, 910120, 910121, 910122)
	a, err := m.Create(ctx, 910120, "it-kill-a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sessions["it-kill-a"] = a
	b, err := m.Create(ctx, 910121, "it-kill-b", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sessions["it-kill-b"] = b
	running := func(id string) bool { ci, _ := d.Inspect(ctx, id); return ci.Running }
	if n, err := m.KillUser(ctx, 910120); err != nil || n != 1 || running(a) || !running(b) {
		t.Fatalf("kill user: n=%d err=%v", n, err)
	}
	if _, err := m.Create(ctx, 910120, "it-kill-a2", nil, false); !errors.Is(err, ErrKilled) {
		t.Fatalf("a stopped user can start sandboxes: %v", err)
	}
	c, err := m.Create(ctx, 910122, "it-kill-c", nil, false) // another user is not blocked
	if err != nil {
		t.Fatal(err)
	}
	sessions["it-kill-c"] = c
	m.ReleaseUser(910120)
	a3, err := m.Create(ctx, 910120, "it-kill-a3", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sessions["it-kill-a3"] = a3
	if n, _ := m.KillAll(ctx); n < 1 || running(b) || running(a3) {
		t.Fatalf("kill all stopped %d", n)
	}
	if _, err := m.Exec(ctx, "it-kill-b", b, ExecSpec{Cmd: []string{"true"}}); !errors.Is(err, ErrKilled) {
		t.Fatalf("exec during the kill switch: %v", err)
	}
	m.ReleaseKill()
	d2, err := m.Create(ctx, 910121, "it-kill-d", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sessions["it-kill-d"] = d2
}
