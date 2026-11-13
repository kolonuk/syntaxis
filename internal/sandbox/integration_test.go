// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Integration tests run against a real Docker host with gVisor registered.
// They skip themselves when Docker, the runsc runtime, or the sandbox image is
// absent, so `go test ./...` stays green everywhere. Select with -run Integration.
func integrationManager(t *testing.T) (*Manager, *Docker) {
	t.Helper()
	d, err := NewDocker("")
	if err != nil {
		t.Skip("no docker client: " + err.Error())
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip("docker not reachable: " + err.Error())
	}
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("runsc (gVisor) is not registered with Docker")
	}
	cfg := DefaultConfig()
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("sandbox image " + cfg.Image + " not built (docker build -t syntaxis-sandbox:latest images/sandbox)")
	}
	return NewManager(d, cfg, nil), d
}

func TestIntegrationGVisorIsolation(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uidA, uidB = 910001, 910002
	idA, err := m.Create(ctx, uidA, "it-a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := m.Create(ctx, uidB, "it-b", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.Destroy(ctx, "it-a", idA)
		_ = m.Destroy(ctx, "it-b", idB)
		_ = d.RemoveVolume(ctx, WorkspaceVolume(uidA))
		_ = d.RemoveVolume(ctx, WorkspaceVolume(uidB))
	})
	sh := func(id, session, script string) ExecResult {
		r, err := m.Exec(ctx, session, id, ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 30 * time.Second})
		if err != nil && !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
		return r
	}
	if r := sh(idA, "it-a", "id -u"); strings.TrimSpace(r.Stdout) != "10001" {
		t.Fatalf("not running as the non-root user: %q", r.Stdout)
	}
	if r := sh(idA, "it-a", "echo x > /etc/syntaxis-test"); r.ExitCode == 0 {
		t.Fatal("root filesystem is writable")
	}
	if r := sh(idA, "it-a", "echo alice > /workspace/secret.txt && cat /workspace/secret.txt"); strings.TrimSpace(r.Stdout) != "alice" {
		t.Fatalf("workspace not writable: %+v", r)
	}
	if r := sh(idB, "it-b", "cat /workspace/secret.txt"); r.ExitCode == 0 || strings.Contains(r.Stdout, "alice") {
		t.Fatalf("user B read user A's workspace: %+v", r)
	}
	if r := sh(idA, "it-a", "grep -i capeff /proc/self/status"); !strings.Contains(r.Stdout, "0000000000000000") {
		t.Fatalf("capabilities not dropped: %q", r.Stdout)
	}
	if r := sh(idA, "it-a", "ls /var/run/docker.sock 2>&1"); r.ExitCode == 0 {
		t.Fatal("a docker socket is visible inside the sandbox")
	}
}

// With the egress firewall applied (scripts/apply-egress.sh) the host side of
// these checks must fail. Without it they are reported, not asserted.
func TestIntegrationForkBombContainedByPidsLimit(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uid = 910003
	id, err := m.Create(ctx, uid, "it-fork", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, "it-fork", id); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid)) })
	r, _ := m.Exec(ctx, "it-fork", id, ExecSpec{Cmd: []string{"sh", "-c", ":(){ :|:& };: ; sleep 3"}, Timeout: 20 * time.Second})
	_ = r
	st, err := d.Stats(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.PidsLimit > 0 && st.Pids > st.PidsLimit {
		t.Fatalf("pids %d exceeded limit %d", st.Pids, st.PidsLimit)
	}
	// the host (this test) is still responsive and the container is still manageable
	if err := m.Stop(ctx, id); err != nil {
		t.Fatalf("container unmanageable after fork bomb: %v", err)
	}
}
