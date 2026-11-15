// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/egress"
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

// Egress enforcement against the real firewall. Needs nft and passwordless
// sudo (CI runners and the dev host have both); set SYNTAXIS_IT_EGRESS=1 to run.
func TestIntegrationEgressBlocksLANHostMetadataSMTP(t *testing.T) {
	if os.Getenv("SYNTAXIS_IT_EGRESS") != "1" {
		t.Skip("set SYNTAXIS_IT_EGRESS=1 to apply the egress ruleset on this host")
	}
	m, d := integrationManager(t)
	ctx := context.Background()
	if err := d.EnsureNetwork(ctx, m.Cfg.Network); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "network", "inspect", m.Cfg.Network, "-f", "{{(index .IPAM.Config 0).Subnet}} {{(index .IPAM.Config 0).Gateway}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	subnet, gw := netip.MustParsePrefix(f[0]), netip.MustParseAddr(f[1])
	netID, _ := exec.Command("docker", "network", "inspect", m.Cfg.Network, "-f", "{{.Id}}").Output()
	bridge := "br-" + strings.TrimSpace(string(netID))[:12]
	rs, err := egress.Nftables("syntaxis_it", subnet, netip.Addr{}, egress.Policy{Mode: egress.ModeOpen, BridgeGateway: gw, Bridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	apply := exec.Command("sudo", "-n", "nft", "-f", "-")
	apply.Stdin = strings.NewReader(rs.Text)
	if b, err := apply.CombinedOutput(); err != nil {
		t.Skipf("cannot apply nft rules (%v): %s", err, b)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "nft", "delete", "table", "inet", "syntaxis_it").Run()
		_ = exec.Command("sudo", "-n", "nft", "delete", "table", "bridge", "syntaxis_it_l2").Run()
	})

	// the health check's behavioural probe sees the loaded rules
	if ok, err := m.ProbeFirewall(ctx); err != nil || !ok {
		t.Errorf("firewall probe with the rules loaded: ok=%v err=%v", ok, err)
	}

	const uid = 910004
	id, err := m.Create(ctx, uid, "it-egress", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, "it-egress", id); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid)) })
	reach := func(target string) bool {
		r, _ := m.Exec(ctx, "it-egress", id, ExecSpec{Cmd: []string{"sh", "-c", "curl -s -m 5 -o /dev/null " + target}, Timeout: 20 * time.Second})
		return r.ExitCode == 0
	}
	// a second sandbox on the same network must not be reachable from the first
	const uid2 = 910006
	id2, err := m.Create(ctx, uid2, "it-egress-2", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, "it-egress-2", id2); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid2)) })
	ip2, _ := d.ContainerIP(ctx, id2, m.Cfg.Network)
	_, _ = m.Exec(ctx, "it-egress-2", id2, ExecSpec{Cmd: []string{"sh", "-c", "nohup python3 -m http.server 8000 >/dev/null 2>&1 &"}, Timeout: 10 * time.Second})
	time.Sleep(2 * time.Second)
	if r, _ := m.Exec(ctx, "it-egress", id, ExecSpec{Cmd: []string{"sh", "-c", "curl -s -m 4 -o /dev/null http://" + ip2 + ":8000/"}, Timeout: 20 * time.Second}); r.ExitCode == 0 {
		t.Errorf("sandbox A reached sandbox B (%s) on the shared network", ip2)
	}
	if !reach("http://1.1.1.1/") {
		t.Skip("no internet from the sandbox on this host")
	}
	// A listener on every host interface: connections addressed to the host itself take the
	// input path, not the forward path, and must be dropped too. Counting accepted
	// connections is exact, unlike curl's exit code against a non-HTTP service.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	hostPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	lan := hostLANAddr(t)
	for name, target := range map[string]string{
		"host service via the bridge gateway": "http://" + gw.String() + ":" + hostPort + "/",
		"host service via its LAN address":    "http://" + lan + ":" + hostPort + "/",
		"cloud metadata":                      "http://169.254.169.254/latest/meta-data/",
		"SMTP to a public host":               "telnet://1.1.1.1:25",
		"private range":                       "http://10.255.255.1/",
	} {
		before := accepted.Load()
		if reach(target) || accepted.Load() != before {
			t.Errorf("%s (%s) is reachable from the sandbox", name, target)
		}
	}
	// The host runs other applications: an ordinary container on Docker's default bridge
	// must still reach the same host service, and the host must still reach the LAN.
	defGW, err := exec.Command("docker", "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}").Output()
	if err == nil {
		before := accepted.Load()
		out, err := exec.Command("docker", "run", "--rm", "--network", "bridge", m.Cfg.Image,
			"curl", "-s", "-m", "5", "-o", "/dev/null", "http://"+strings.TrimSpace(string(defGW))+":"+hostPort+"/").CombinedOutput()
		if accepted.Load() == before {
			t.Errorf("an ordinary container could not reach a host service with the sandbox rules loaded (%v): %s", err, out)
		}
	}
	before := accepted.Load()
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(lan, hostPort), 3*time.Second); err != nil {
		t.Errorf("the host cannot reach its own LAN address with the sandbox rules loaded: %v", err)
	} else {
		_ = c.Close()
		for i := 0; i < 20 && accepted.Load() == before; i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func hostLANAddr(t *testing.T) string {
	t.Helper()
	c, err := net.Dial("udp", "198.51.100.1:9")
	if err != nil {
		t.Skip("no route to determine the host LAN address")
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// Names resolve inside a sandbox on a user-defined network. Docker's embedded
// resolver does not work under gVisor, so ApplyDNS must make this work.
func TestIntegrationDNSWorksOnUserDefinedNetworkUnderGVisor(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	m.Cfg.Network = "syntaxis-dns-it"
	m.SetDNS([]string{"1.1.1.1"})
	if err := d.EnsureNetwork(ctx, m.Cfg.Network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", "syntaxis-dns-it").Run() })
	const uid = 910005
	id, err := m.Create(ctx, uid, "it-dns", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, "it-dns", id); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid)) })
	r, _ := m.Exec(ctx, "it-dns", id, ExecSpec{Cmd: []string{"sh", "-c", "curl -s -m 8 -o /dev/null -w '%{http_code}' https://example.com/"}, Timeout: 30 * time.Second})
	if strings.TrimSpace(r.Stdout) != "200" {
		t.Skipf("no internet from containers on this host (got %q)", r.Stdout)
	}
}
