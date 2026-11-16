// SPDX-License-Identifier: GPL-2.0-or-later

package doctor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/dockerproxy"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

func TestIsPublicBind(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8081": false, "[::1]:8081": false, "10.1.2.3:8081": false, "192.168.1.5:80": false, ":8081": true, "0.0.0.0:8081": true, "[::]:8081": true, "203.0.113.9:8081": true, "localhost:8081": false} {
		if isPublicBind(addr) != want {
			t.Errorf("%s: want %v", addr, !want)
		}
	}
}

func TestWithin(t *testing.T) {
	if !within("/data/master.key", "/data") || within("/etc/master.key", "/data") || within("/data/../etc/k", "/data") {
		t.Fatal("within")
	}
}

// realDocker returns the real Docker engine, or skips the test.
func realDocker(t *testing.T) *sandbox.Docker {
	t.Helper()
	testenv.Docker(t)
	d, err := sandbox.NewDocker("")
	if err != nil {
		testenv.NotTested(t, "no Docker client: "+err.Error())
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// End to end against the real Ollama and the real Docker: collection and the health engine, to the
// findings the admin would see.
func TestCollectAndEvaluateAgainstRealServices(t *testing.T) {
	env := testenv.Load(t)
	rt := realDocker(t)
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "d.db"))
	defer st.Close()
	ctx := context.Background()
	pid, _ := st.UpsertProvider(ctx, store.Provider{Name: "p", Kind: env["OLLAMA_KIND"], BaseURL: env["OLLAMA_URL"], Enabled: true})
	for _, n := range []string{env["MODEL_TINY"], "no-such-model-on-the-server"} {
		id, _ := st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: n, ToolsSupported: -1})
		_ = st.SetModelEnabled(ctx, id, true)
	}
	cfg := &config.Config{AdminListen: "0.0.0.0:8081", EgressMode: "open", SecretScanMode: "off", BaseURL: "http://localhost:8080"}
	cfg.Docker.Runtime, cfg.Docker.Image = "syntaxis-no-such-runtime", "syntaxis-no-such-image:latest" // not on this Docker
	d := Deps{Cfg: cfg, Store: st, Runtime: rt, NewProv: func(p store.Provider) (provider.Provider, error) {
		return provider.New(provider.Config{Kind: p.Kind, BaseURL: p.BaseURL})
	}}
	state := Collect(ctx, d)
	if len(state.Providers) != 1 || !state.Providers[0].Reachable || !state.Sandbox.DockerReachable {
		t.Fatalf("both services are real and up: %+v %+v", state.Providers, state.Sandbox)
	}
	fs := health.NewEngine(health.DefaultThresholds()).Run(state)
	have := map[string]bool{}
	for _, f := range fs {
		have[f.CheckID] = true
	}
	for _, id := range []string{
		health.CheckModelGone,    // an enabled model the server does not list
		health.CheckRunsc,        // the named runtime is not registered
		health.CheckAdminExposed, // admin bound to 0.0.0.0
		health.CheckSMTP, health.CheckMasterKey, health.CheckScannerOff,
		health.CheckConcurrency, // concurrency unset
	} {
		if !have[id] {
			t.Errorf("expected finding %s; got %v", id, ids(fs))
		}
	}
	var tiny health.ModelState
	for _, m := range state.Providers[0].Models {
		if m.Name == env["MODEL_TINY"] {
			tiny = m
		}
	}
	if tiny.ContextAdvert <= 0 || tiny.ContextSource == "" {
		t.Fatalf("the real model's window was not detected: %+v", tiny)
	}
	if b, _ := json.Marshal(state); len(b) == 0 {
		t.Fatal("state not serialisable")
	}
}

func ids(fs []health.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.CheckID+"@"+f.Subject)
	}
	return out
}

func TestUnreachableProviderCollected(t *testing.T) {
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "d.db"))
	defer st.Close()
	ctx := context.Background()
	_, _ = st.UpsertProvider(ctx, store.Provider{Name: "p", Kind: "ollama", BaseURL: "http://127.0.0.1:1", Enabled: true})
	d := Deps{Cfg: &config.Config{}, Store: st, NewProv: func(p store.Provider) (provider.Provider, error) {
		return provider.New(provider.Config{Kind: p.Kind, BaseURL: p.BaseURL})
	}}
	state := Collect(ctx, d)
	if len(state.Providers) != 1 || state.Providers[0].Reachable || state.Providers[0].Err == "" {
		t.Fatalf("%+v", state.Providers)
	}
	fs := health.NewEngine(health.DefaultThresholds()).Run(state)
	found := false
	for _, f := range fs {
		if f.CheckID == health.CheckProviderDown && f.UserMessage != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("unreachable provider not reported with a user banner")
	}
}

func TestNoDockerReported(t *testing.T) {
	state := Collect(context.Background(), Deps{Cfg: &config.Config{}})
	if state.Sandbox.DockerReachable || state.Sandbox.DockerErr == "" {
		t.Fatalf("%+v", state.Sandbox)
	}
}

// A generic socket proxy lets a compromised server create privileged containers; the
// health check must notice when the validating proxy is not the one in front of Docker. Both
// kinds are real here: the Syntaxis proxy, and a plain byte-for-byte forwarder to the same Docker.
func TestProxyStrictnessProbe(t *testing.T) {
	testenv.Docker(t)
	sock := "/var/run/docker.sock"
	if _, err := os.Stat(sock); err != nil {
		testenv.NotTested(t, "the Docker socket is not at "+sock)
	}
	dial := func(context.Context) (net.Conn, error) { return net.Dial("unix", sock) }

	strict := httptest.NewServer(dockerproxy.New(dockerproxy.Config{Network: "syntaxis-sandbox", Dial: dial}))
	defer strict.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				u, err := dial(context.Background())
				if err != nil {
					_ = c.Close()
					return
				}
				go func() { _, _ = io.Copy(u, c); _ = u.Close() }()
				_, _ = io.Copy(c, u)
				_ = c.Close()
			}()
		}
	}()

	tcp := func(addr string) string { return "tcp://" + strings.TrimPrefix(addr, "http://") }
	if ProxyIsStrict(context.Background(), tcp(l.Addr().String())) {
		t.Fatal("a plain forwarder was reported as strict")
	}
	if !ProxyIsStrict(context.Background(), tcp(strict.URL)) {
		t.Fatal("the strict proxy was not recognised")
	}
}

// The firewall finding must follow what a real test sandbox measured, not a setting.
func TestFirewallProbeDecidesTheNetworkFinding(t *testing.T) {
	testenv.GVisor(t)
	rt := realDocker(t)
	cfg := &config.Config{EgressMode: "open"}
	scfg := sandbox.DefaultConfig()
	cfg.Docker.Runtime, cfg.Docker.Image = scfg.Runtime, scfg.Image
	ctx := context.Background()
	if ok, _ := rt.ImagePresent(ctx, scfg.Image); !ok {
		testenv.NotTested(t, "the sandbox image "+scfg.Image+" is not built")
	}
	t.Setenv("SYNTAXIS_NETWORK_RULES_APPLIED", "true") // a stale statement must not win over the probe
	mgr := sandbox.NewManager(rt, scfg, nil)
	d := Deps{Cfg: cfg, Runtime: rt, ProbeFirewall: mgr.ProbeFirewall}
	state := Collect(ctx, d)
	if !state.Sandbox.NetworkRulesProbed {
		t.Fatal("the probe result was not used")
	}
	found := false
	for _, f := range health.NewEngine(health.DefaultThresholds()).Run(state) {
		if f.CheckID == health.CheckNetworkRules {
			found = true
			if !strings.Contains(f.Message, "test sandbox reached the Docker host") {
				t.Fatalf("message: %s", f.Message)
			}
		}
	}
	// the finding appears exactly when the real probe found the rules NOT loaded
	if found == state.Sandbox.NetworkRulesApplied {
		t.Fatalf("rules loaded=%v but finding=%v", state.Sandbox.NetworkRulesApplied, found)
	}
}

// Inside the Compose container the admin app listens on 0.0.0.0, but the host publishes
// it on the address in SYNTAXIS_ADMIN_BIND; that is what decides exposure.
func TestAdminExposureUsesThePublishedBind(t *testing.T) {
	cfg := &config.Config{AdminListen: "0.0.0.0:8081"}
	for bind, public := range map[string]bool{"127.0.0.1": false, "0.0.0.0": true, "": true} {
		t.Setenv("SYNTAXIS_ADMIN_BIND", bind)
		if got := Collect(context.Background(), Deps{Cfg: cfg}).Security.AdminListenPublic; got != public {
			t.Errorf("bind %q: public=%v, want %v", bind, got, public)
		}
	}
}
