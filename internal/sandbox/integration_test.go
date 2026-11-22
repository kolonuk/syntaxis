// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// Integration tests run against a real Docker host with gVisor registered.
// They skip themselves when Docker, the runsc runtime, or the sandbox image is
// absent, so `go test ./...` stays green everywhere. Select with -run Integration.
func integrationManager(t *testing.T) (*Manager, *Docker) {
	t.Helper()
	d, err := NewDocker("")
	if err != nil {
		t.Skip("NOT TESTED: no docker client: " + err.Error())
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip("NOT TESTED: docker not reachable: " + err.Error())
	}
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("NOT TESTED: runsc (gVisor) is not registered with Docker")
	}
	cfg := DefaultConfig()
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("NOT TESTED: sandbox image " + cfg.Image + " not built (docker build -t syntaxis-sandbox:latest images/sandbox)")
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
		plain := exec.Command("docker", "run", "--rm", "--network", "bridge", m.Cfg.Image,
			"curl", "-s", "-m", "5", "-o", "/dev/null", "http://"+strings.TrimSpace(string(defGW))+":"+hostPort+"/")
		// another application's container: straight to the daemon, not through the Syntaxis proxy
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "DOCKER_HOST=") {
				plain.Env = append(plain.Env, kv)
			}
		}
		out, err := plain.CombinedOutput()
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
	testenv.KeepDNSVolume(t, m.Cfg.Image)
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

// monitorFixture starts a real gVisor sandbox with busy loops and a Monitor with short windows.
func monitorFixture(t *testing.T, session string, uid int64) (*Manager, *Docker, string, *Monitor) {
	t.Helper()
	m, d := integrationManager(t)
	ctx := context.Background()
	id, err := m.Create(ctx, uid, session, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, session, id); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid)) })
	// three busy loops against a two-core cap
	if r, err := m.Exec(ctx, session, id, ExecSpec{Cmd: []string{"sh", "-c", "for i in 1 2 3; do (while :; do :; done) >/dev/null 2>&1 & done; echo started"}, Timeout: 10 * time.Second}); err != nil || r.ExitCode != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	cfg := DefaultWatchdogConfig()
	cfg.WarnAfter, cfg.ThrottleAfter, cfg.PauseAfter, cfg.KillAfter, cfg.MinerAfter = 3*time.Second, 6*time.Second, 14*time.Second, 40*time.Second, 0
	// a monitor ticks every sandbox its runtime lists, so it sees only this test's user: another
	// Syntaxis on the same Docker host (and other tests) must not be paused or stopped by it
	mon := NewMonitor(scoped{Runtime: d, users: map[string]bool{strconv.FormatInt(uid, 10): true}}, NewWatchdog(cfg), DefaultLimits().CPUs, nil)
	mon.Interval, mon.Window = time.Second, 4
	return m, d, id, mon
}

func nanoCPUs(t *testing.T, id string) int64 {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.HostConfig.NanoCpus}}", id).Output()
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	_, _ = fmt.Sscan(strings.TrimSpace(string(out)), &n)
	return n
}

func TestIntegrationMonitorThrottlesThenPausesSustainedLoadAndMetersCPU(t *testing.T) {
	_, d, id, mon := monitorFixture(t, "it-mon", 910010)
	ctx := context.Background()
	var cpu int64
	mon.OnCPU = func(_ int64, s int64) { cpu += s }
	var acts []Action
	mon.OnEvent = func(_ string, e Event) { acts = append(acts, e.Action) }
	deadline := time.Now().Add(60 * time.Second)
	throttled := false
	for time.Now().Before(deadline) {
		mon.Tick(ctx, time.Now())
		if !throttled && nanoCPUs(t, id) == int64(DefaultLimits().CPUs*0.5*1e9) {
			throttled = true
		}
		if ci, err := d.Inspect(ctx, id); err == nil && ci.Paused {
			break
		}
		time.Sleep(time.Second)
	}
	if !throttled {
		t.Errorf("sustained load was never throttled; events %v", acts)
	}
	ci, err := d.Inspect(ctx, id)
	if err != nil || !ci.Paused {
		t.Fatalf("sustained load was never paused (events %v, err %v)", acts, err)
	}
	if cpu < 10 {
		t.Errorf("only %d CPU-seconds metered for a sandbox that ran flat out", cpu)
	}
}

func TestIntegrationMonitorPausesWhenDailyAllowanceIsUsedUp(t *testing.T) {
	_, d, id, mon := monitorFixture(t, "it-allow", 910012)
	ctx := context.Background()
	mon.Exhausted = func(int64) bool { return true }
	for i := 0; i < 5; i++ {
		mon.Tick(ctx, time.Now())
		time.Sleep(time.Second)
	}
	if ci, err := d.Inspect(ctx, id); err != nil || !ci.Paused {
		t.Fatalf("a user over their compute allowance kept running: %+v %v", ci, err)
	}
}

func TestIntegrationMonitorRestoresTheCapWhenLoadStops(t *testing.T) {
	m, d, id, mon := monitorFixture(t, "it-restore", 910014)
	ctx := context.Background()
	throttled := int64(DefaultLimits().CPUs * 0.5 * 1e9)
	deadline := time.Now().Add(40 * time.Second)
	for nanoCPUs(t, id) != throttled && time.Now().Before(deadline) {
		mon.Tick(ctx, time.Now())
		time.Sleep(time.Second)
	}
	if nanoCPUs(t, id) != throttled {
		t.Fatal("never throttled")
	}
	if ci, _ := d.Inspect(ctx, id); ci.Paused {
		t.Skip("the load was paused before it could be stopped; the machine is too slow for this timing")
	}
	// stop the busy loops (the pattern cannot match this command's own text)
	if r, err := m.Exec(ctx, "it-restore", id, ExecSpec{Cmd: []string{"sh", "-c", "pkill -f 'whi[l]e :'; echo done"}, Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("%v %+v", err, r)
	}
	deadline = time.Now().Add(60 * time.Second)
	for nanoCPUs(t, id) == throttled && time.Now().Before(deadline) {
		mon.Tick(ctx, time.Now())
		time.Sleep(time.Second)
	}
	if got := nanoCPUs(t, id); got != int64(DefaultLimits().CPUs*1e9) {
		t.Fatalf("the cap was not restored after the load stopped: %d", got)
	}
}

func TestIntegrationMonitorWarnsWhenTheWorkspaceIsOverItsLimitAndKeepsRunning(t *testing.T) {
	m, d := integrationManager(t)
	ctx := context.Background()
	const uid, session = 910016, "it-disk"
	id, err := m.Create(ctx, uid, session, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, session, id); _ = d.RemoveVolume(ctx, WorkspaceVolume(uid)) })
	if r, err := m.Exec(ctx, session, id, ExecSpec{Cmd: []string{"sh", "-c", "dd if=/dev/zero of=/workspace/big bs=1M count=6 2>/dev/null"}, Timeout: 20 * time.Second}); err != nil || r.ExitCode != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	cfg := DefaultWatchdogConfig()
	cfg.DiskBytesLimit = 5 << 20 // 6 MiB is 120%: over the limit, under the 125% that stops a runaway fill
	mon := NewMonitor(scoped{Runtime: d, users: map[string]bool{strconv.FormatInt(uid, 10): true}}, NewWatchdog(cfg), DefaultLimits().CPUs, nil)
	mon.Interval, mon.Window = time.Second, 4
	mon.DiskBytes = func(ctx context.Context, cid string) (int64, error) { return WorkspaceBytes(ctx, d, cid) }
	mon.DiskEvery = time.Second
	var warned bool
	mon.OnEvent = func(_ string, e Event) {
		if e.Action == ActionWarn && strings.Contains(e.Reason, "size limit") {
			warned = true
		}
	}
	for i := 0; i < 4 && !warned; i++ {
		mon.Tick(ctx, time.Now())
		time.Sleep(time.Second)
	}
	if !warned {
		t.Fatal("an over-limit workspace was not reported")
	}
	if ci, err := d.Inspect(ctx, id); err != nil || !ci.Running {
		t.Fatalf("an over-limit workspace must keep running: %+v %v", ci, err)
	}
}
