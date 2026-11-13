// SPDX-License-Identifier: GPL-2.0-or-later

package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
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

func fakeOllama() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"small","digest":"d1"},{"name":"alias","digest":"d1"}]}`)
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model_info":{"x.context_length":131072},"capabilities":["completion"]}`)
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"small","size":100,"size_vram":100,"context_length":4096}]}`)
	})
	return httptest.NewServer(mux)
}

// End to end: real adapter against a fake Ollama, through collection and the
// health engine, to the findings the admin would see.
func TestCollectAndEvaluateAgainstFakeProvider(t *testing.T) {
	srv := fakeOllama()
	defer srv.Close()
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "d.db"))
	defer st.Close()
	ctx := context.Background()
	pid, _ := st.UpsertProvider(ctx, store.Provider{Name: "p", Kind: "ollama", BaseURL: srv.URL, Enabled: true})
	for _, n := range []string{"small", "alias", "gone"} {
		id, _ := st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: n, ToolsSupported: -1})
		_ = st.SetModelEnabled(ctx, id, true)
	}
	cfg := &config.Config{AdminListen: "0.0.0.0:8081", EgressMode: "open", SecretScanMode: "off", BaseURL: "http://localhost:8080"}
	rt := sandbox.NewFake()
	rt.Runtimes = map[string]bool{} // runsc missing
	cfg.Docker.Runtime, cfg.Docker.Image = "runsc", "syntaxis-sandbox:latest"
	d := Deps{Cfg: cfg, Store: st, Runtime: rt, NewProv: func(p store.Provider) (provider.Provider, error) {
		return provider.New(provider.Config{Kind: p.Kind, BaseURL: p.BaseURL})
	}}
	state := Collect(ctx, d)
	fs := health.NewEngine(health.DefaultThresholds()).Run(state)
	have := map[string]bool{}
	for _, f := range fs {
		have[f.CheckID] = true
	}
	for _, id := range []string{
		health.CheckContextSize,  // loaded window 4096 is below the minimum
		health.CheckToolCalling,  // capabilities lacked "tools"
		health.CheckModelGone,    // enabled model not listed by the server
		health.CheckRunsc,        // runsc not registered
		health.CheckAdminExposed, // admin bound to 0.0.0.0
		health.CheckSMTP, health.CheckMasterKey, health.CheckScannerOff, health.CheckNetworkRules,
		health.CheckConcurrency, // concurrency unset
	} {
		if !have[id] {
			t.Errorf("expected finding %s; got %v", id, ids(fs))
		}
	}
	var small health.ModelState
	for _, m := range state.Providers[0].Models {
		if m.Name == "small" {
			small = m
		}
	}
	if small.ContextLoaded != 4096 || small.ContextAdvert != 131072 || small.ContextSource == "" {
		t.Fatalf("%+v", small)
	}
	b, _ := json.Marshal(state)
	if len(b) == 0 {
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
