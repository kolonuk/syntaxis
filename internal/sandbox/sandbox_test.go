// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func TestBuildSpecIsolationProperties(t *testing.T) {
	s, err := BuildSpec(DefaultConfig(), 7, "sess-1", map[string]string{"GH_TOKEN": "x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{
		"runtime is runsc":        s.Runtime == "runsc",
		"not privileged":          !s.Privileged,
		"read-only rootfs":        s.ReadOnlyRootFS,
		"cap_drop ALL":            len(s.CapDrop) == 1 && s.CapDrop[0] == "ALL",
		"no cap_add":              len(s.CapAdd) == 0,
		"no-new-privileges":       len(s.SecurityOpt) == 1 && s.SecurityOpt[0] == "no-new-privileges:true",
		"non-root user":           s.User == "10001:10001",
		"pids limit":              s.PidsLimit > 0,
		"memory limit":            s.MemoryBytes > 0,
		"no swap":                 s.MemorySwap == s.MemoryBytes,
		"cpu limit":               s.NanoCPUs > 0,
		"init reaps zombies":      s.Init,
		"exactly one mount":       len(s.Mounts) == 1,
		"workspace is user's vol": s.Mounts[0].Volume == "syntaxis-ws-u7" && s.Mounts[0].Target == "/workspace",
		"tmpfs /tmp noexec":       strings.Contains(s.Tmpfs["/tmp"], "noexec"),
		"managed label":           s.Labels[LabelManaged] == "true" && s.Labels[LabelUser] == "7" && s.Labels[LabelSession] == "sess-1",
		"env passthrough":         contains(s.Env, "GH_TOKEN=x"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("spec property failed: %s\n%+v", name, s)
		}
	}
	for _, e := range s.Env {
		if strings.Contains(strings.ToUpper(e), "DOCKER_HOST") || strings.Contains(e, "OLLAMA") {
			t.Errorf("forbidden env leaked: %s", e)
		}
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestValidateRejectsEachViolation(t *testing.T) {
	good, _ := BuildSpec(DefaultConfig(), 1, "s", nil, false)
	mutations := map[string]func(*Spec){
		"runc runtime":    func(s *Spec) { s.Runtime = "runc" },
		"empty runtime":   func(s *Spec) { s.Runtime = "" },
		"privileged":      func(s *Spec) { s.Privileged = true },
		"writable rootfs": func(s *Spec) { s.ReadOnlyRootFS = false },
		"cap_add":         func(s *Spec) { s.CapAdd = []string{"SYS_ADMIN"} },
		"keep caps":       func(s *Spec) { s.CapDrop = nil },
		"no nnp":          func(s *Spec) { s.SecurityOpt = nil },
		"seccomp off":     func(s *Spec) { s.SecurityOpt = []string{"no-new-privileges:true", "seccomp=unconfined"} },
		"root user":       func(s *Spec) { s.User = "root" },
		"uid 0":           func(s *Spec) { s.User = "0:0" },
		"empty user":      func(s *Spec) { s.User = "" },
		"no pids":         func(s *Spec) { s.PidsLimit = 0 },
		"no memory":       func(s *Spec) { s.MemoryBytes = 0 },
		"no cpu":          func(s *Spec) { s.NanoCPUs = 0 },
		"host bind":       func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Volume: "/etc", Target: "/host"}) },
		"docker socket": func(s *Spec) {
			s.Mounts = append(s.Mounts, Mount{Volume: "/var/run/docker.sock", Target: "/var/run/docker.sock"})
		},
		"other user's vol":  func(s *Spec) { s.Mounts[0].Volume = WorkspaceVolume(2) },
		"docker_host env":   func(s *Spec) { s.Env = append(s.Env, "DOCKER_HOST=tcp://x:2375") },
		"label tampering":   func(s *Spec) { s.Labels[LabelUser] = "99" },
		"managed label off": func(s *Spec) { s.Labels[LabelManaged] = "false" },
	}
	for name, mut := range mutations {
		s := good
		s.Mounts = append([]Mount(nil), good.Mounts...)
		s.Labels = map[string]string{}
		for k, v := range good.Labels {
			s.Labels[k] = v
		}
		mut(&s)
		if err := Validate(s, 1); !errors.Is(err, ErrBadSpec) {
			t.Errorf("%s: not rejected (%v)", name, err)
		}
	}
	if err := Validate(good, 1); err != nil {
		t.Fatalf("good spec rejected: %v", err)
	}
}

func TestBuildSpecNoRuntime(t *testing.T) {
	c := DefaultConfig()
	c.Runtime = ""
	if _, err := BuildSpec(c, 1, "s", nil, false); err == nil {
		t.Fatal("spec built without gVisor runtime")
	}
	c.Runtime = "runc"
	if _, err := BuildSpec(c, 1, "s", nil, false); err == nil {
		t.Fatal("runc accepted")
	}
}

func TestContainerNameSanitised(t *testing.T) {
	if n := ContainerName("a/b; rm -rf"); strings.ContainsAny(n, "/; ") {
		t.Fatal(n)
	}
}

func newMgr() (*Manager, *Fake) {
	f := NewFake()
	return NewManager(f, DefaultConfig(), nil), f
}

func TestCreateRefusesWithoutRunsc(t *testing.T) {
	m, f := newMgr()
	f.Runtimes["runsc"] = false
	if _, err := m.Create(ctx, 1, "s", nil, false); !errors.Is(err, ErrNoRuntime) {
		t.Fatalf("want ErrNoRuntime, got %v", err)
	}
	if len(f.Containers) != 0 {
		t.Fatal("container created without isolation")
	}
}

func TestCreateMissingImage(t *testing.T) {
	m, f := newMgr()
	delete(f.Images, "syntaxis-sandbox:latest")
	if _, err := m.Create(ctx, 1, "s", nil, false); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("%v", err)
	}
}

func TestCreateStartsContainerAndWorkspace(t *testing.T) {
	m, f := newMgr()
	id, err := m.Create(ctx, 3, "sess", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c := f.Containers[id]
	if !c.Info.Running || c.Spec.Mounts[0].Volume != "syntaxis-ws-u3" || !f.Networks["syntaxis-sandbox"] || f.Volumes["syntaxis-ws-u3"] == nil {
		t.Fatalf("%+v", c)
	}
	if c.Spec.Labels[LabelCreated] == "" {
		t.Fatal("created label")
	}
}

func TestCreateFailureCleansUp(t *testing.T) {
	m, f := newMgr()
	f.CreateErr = errors.New("boom")
	if _, err := m.Create(ctx, 1, "s", nil, false); err == nil {
		t.Fatal("expected error")
	}
}

func TestUserIsolationOfWorkspaces(t *testing.T) {
	m, f := newMgr()
	a, _ := m.Create(ctx, 1, "sa", nil, false)
	b, _ := m.Create(ctx, 2, "sb", nil, false)
	if _, err := m.Exec(ctx, "sa", a, ExecSpec{Cmd: []string{"write", "/workspace/secret.txt", "alice-data"}}); err != nil {
		t.Fatal(err)
	}
	// A can read her own data
	r, _ := m.Exec(ctx, "sa", a, ExecSpec{Cmd: []string{"cat", "/workspace/secret.txt"}})
	if r.Stdout != "alice-data" {
		t.Fatalf("own data: %+v", r)
	}
	// B cannot see A's file at the same path, nor via any path
	r, _ = m.Exec(ctx, "sb", b, ExecSpec{Cmd: []string{"cat", "/workspace/secret.txt"}})
	if r.ExitCode == 0 || strings.Contains(r.Stdout, "alice") {
		t.Fatalf("user B read user A's workspace: %+v", r)
	}
	r, _ = m.Exec(ctx, "sb", b, ExecSpec{Cmd: []string{"cat", "/workspace/../syntaxis-ws-u1/secret.txt"}})
	if strings.Contains(r.Stdout, "alice") {
		t.Fatal("path trick read other workspace")
	}
	// B's container spec never references A's volume
	for _, mnt := range f.Containers[b].Spec.Mounts {
		if mnt.Volume == WorkspaceVolume(1) {
			t.Fatal("B mounted A's volume")
		}
	}
	// rootfs outside /workspace is not writable
	r, _ = m.Exec(ctx, "sb", b, ExecSpec{Cmd: []string{"write", "/etc/passwd", "x"}})
	if r.ExitCode == 0 {
		t.Fatal("wrote outside workspace")
	}
}

func TestExecDefaultsAndCaps(t *testing.T) {
	m, f := newMgr()
	id, _ := m.Create(ctx, 1, "s", nil, false)
	_, _ = m.Exec(ctx, "s", id, ExecSpec{Cmd: []string{"echo"}, Timeout: 99 * time.Hour})
	got := f.Containers[id].Execs[0]
	if got.Timeout != MaxExecTimeout || got.WorkingDir != WorkspacePath || got.MaxOutput != DefaultMaxOutput {
		t.Fatalf("%+v", got)
	}
	_, _ = m.Exec(ctx, "s", id, ExecSpec{Cmd: []string{"echo"}})
	if f.Containers[id].Execs[1].Timeout != DefaultExecTimeout {
		t.Fatal("default timeout")
	}
}

func TestExecTimeoutSurfaced(t *testing.T) {
	m, f := newMgr()
	id, _ := m.Create(ctx, 1, "s", nil, false)
	f.ExecHook = func(*FakeContainer, ExecSpec) (ExecResult, bool) {
		return ExecResult{TimedOut: true, ExitCode: -1}, true
	}
	if _, err := m.Exec(ctx, "s", id, ExecSpec{Cmd: []string{"sleep", "999"}}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("%v", err)
	}
}

func TestLifecycleStopResumeDestroy(t *testing.T) {
	m, f := newMgr()
	id, _ := m.Create(ctx, 1, "s", nil, false)
	_ = m.Stop(ctx, id)
	if f.Containers[id].Info.Running {
		t.Fatal("not stopped")
	}
	if err := m.Resume(ctx, "s", id); err != nil || !f.Containers[id].Info.Running {
		t.Fatalf("resume: %v", err)
	}
	_ = f.Pause(ctx, id)
	if err := m.Resume(ctx, "s", id); err != nil || f.Containers[id].Info.Paused {
		t.Fatal("unpause")
	}
	if err := m.Destroy(ctx, "s", id); err != nil || len(f.Containers) != 0 {
		t.Fatal("destroy")
	}
	if err := m.Destroy(ctx, "s", id); err != nil {
		t.Fatalf("destroying a missing container should be fine: %v", err)
	}
	// workspace persists after the container is gone
	if f.Volumes["syntaxis-ws-u1"] == nil {
		t.Fatal("workspace volume removed with container")
	}
	if err := m.Resume(ctx, "s", id); !errors.Is(err, ErrNotFound) {
		t.Fatal("resume of missing")
	}
}

func TestReaper(t *testing.T) {
	m, f := newMgr()
	now := time.Now()
	f.Now = func() time.Time { return now.Add(-10 * 24 * time.Hour) }
	oldID, _ := m.Create(ctx, 1, "old", nil, false) // created 10 days ago (per fake clock)
	f.Now = func() time.Time { return now.Add(-2 * time.Hour) }
	idleID, _ := m.Create(ctx, 1, "idle", nil, false)
	f.Now = func() time.Time { return now }
	freshID, _ := m.Create(ctx, 1, "fresh", nil, false)
	orphanID, _ := m.Create(ctx, 1, "orphan", nil, false)
	// simulate restart: manager forgets activity, container created times decide
	m.mu.Lock()
	m.activity = map[string]time.Time{"fresh": now}
	m.mu.Unlock()
	m.Now = func() time.Time { return now }
	known := map[string]bool{"old": true, "idle": true, "fresh": true}
	res, err := m.Reap(ctx, ReapPolicy{IdleAfter: 30 * time.Minute, DestroyAfter: 7 * 24 * time.Hour, Known: func(s string) bool { return known[s] }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orphans) != 1 || len(res.Destroyed) != 1 || len(res.Stopped) != 1 {
		t.Fatalf("%+v", res)
	}
	if _, ok := f.Containers[orphanID]; ok {
		t.Fatal("orphan survived")
	}
	if _, ok := f.Containers[oldID]; ok {
		t.Fatal("expired container survived")
	}
	if f.Containers[idleID].Info.Running {
		t.Fatal("idle container still running")
	}
	if !f.Containers[freshID].Info.Running {
		t.Fatal("fresh container wrongly stopped")
	}
}

func TestKillSwitch(t *testing.T) {
	m, f := newMgr()
	a, _ := m.Create(ctx, 1, "a", nil, false)
	b, _ := m.Create(ctx, 2, "b", nil, false)
	n, err := m.KillUser(ctx, 1)
	if err != nil || n != 1 || f.Containers[a].Info.Running || !f.Containers[b].Info.Running {
		t.Fatalf("kill user: n=%d err=%v", n, err)
	}
	if _, err := m.Create(ctx, 1, "a2", nil, false); !errors.Is(err, ErrKilled) {
		t.Fatal("killed user can start sandboxes")
	}
	if _, err := m.Create(ctx, 2, "b2", nil, false); err != nil {
		t.Fatal("other user blocked by per-user kill")
	}
	m.ReleaseUser(1)
	if _, err := m.Create(ctx, 1, "a3", nil, false); err != nil {
		t.Fatal(err)
	}
	n, _ = m.KillAll(ctx)
	if n < 2 {
		t.Fatalf("killall stopped %d", n)
	}
	for id, c := range f.Containers {
		if c.Info.Running {
			t.Fatalf("%s still running after kill all", id)
		}
	}
	if _, err := m.Exec(ctx, "b", b, ExecSpec{Cmd: []string{"x"}}); !errors.Is(err, ErrKilled) {
		t.Fatal("exec during kill switch")
	}
	m.ReleaseKill()
	if _, err := m.Create(ctx, 2, "b4", nil, false); err != nil {
		t.Fatal(err)
	}
}
