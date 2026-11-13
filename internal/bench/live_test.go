// SPDX-License-Identifier: GPL-2.0-or-later

package bench

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/doctor"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// Live tests: real local LLM + real gVisor sandbox. They need the private
// environment file and skip everywhere else. Select with -run Live.

func liveProvider(t *testing.T) (provider.Provider, map[string]string) {
	env := testenv.Load(t)
	p, err := provider.New(provider.Config{Kind: env["OLLAMA_KIND"], BaseURL: env["OLLAMA_URL"]})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Ping(ctx); err != nil {
		t.Skip("LLM server not reachable: " + err.Error())
	}
	return p, env
}

func TestLiveDiscoveryAndHealth(t *testing.T) {
	p, env := liveProvider(t)
	ctx := context.Background()
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v %v", ms, err)
	}
	for _, m := range ms {
		d, _ := p.ModelDetail(ctx, m.Name)
		t.Logf("model %-34s advertised_ctx=%d tools=%v params=%s quant=%s", m.Name, d.ContextAdvert, boolPtr(d.SupportsTools), d.ParameterSize, d.Quantisation)
	}
	loaded, supported, _ := p.Loaded(ctx)
	for _, l := range loaded {
		t.Logf("loaded %-34s ctx=%d size=%dMB vram=%dMB", l.Name, l.ContextSize, l.SizeBytes>>20, l.VRAMBytes>>20)
	}
	if !supported {
		t.Fatal("Ollama must report residency")
	}
	// health engine against the real server
	st, _ := store.OpenSQLite(t.TempDir() + "/h.db")
	defer st.Close()
	pid, _ := st.UpsertProvider(ctx, store.Provider{Name: "local", Kind: env["OLLAMA_KIND"], BaseURL: env["OLLAMA_URL"], Concurrency: 2, Enabled: true})
	for _, m := range ms {
		if strings.Contains(m.Name, "embed") {
			continue
		}
		id, _ := st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: m.Name, ToolsSupported: -1})
		_ = st.SetModelEnabled(ctx, id, true)
	}
	cfg := &config.Config{AdminListen: "127.0.0.1:8081", EgressMode: "open", SecretScanMode: "warn"}
	state := doctor.Collect(ctx, doctor.Deps{Cfg: cfg, Store: st, NewProv: func(sp store.Provider) (provider.Provider, error) {
		return provider.New(provider.Config{Kind: sp.Kind, BaseURL: sp.BaseURL})
	}})
	for _, f := range health.NewEngine(health.DefaultThresholds()).Run(state) {
		if strings.HasPrefix(f.CheckID, "context.") || strings.HasPrefix(f.CheckID, "tools.") || strings.HasPrefix(f.CheckID, "provider.") || strings.HasPrefix(f.CheckID, "routing.") || strings.HasPrefix(f.CheckID, "queue.") {
			t.Logf("health [%s] %s %s: %s", f.Severity, f.CheckID, f.Subject, f.Message)
		}
	}
}

func boolPtr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return fmt.Sprint(*b)
}

// The benchmark suite against a real model, in a real gVisor sandbox.
func TestLiveBenchmarkInGVisor(t *testing.T) {
	p, env := liveProvider(t)
	model := os.Getenv("SYNTAXIS_LIVE_MODEL")
	if model == "" {
		model = env["MODEL_MAIN"]
	}
	d, err := sandbox.NewDocker("")
	if err != nil {
		t.Skip("no docker")
	}
	defer d.Close()
	ctx := context.Background()
	cfg := sandbox.DefaultConfig()
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("runsc not registered")
	}
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("sandbox image not built")
	}
	m := sandbox.NewManager(d, cfg, nil)
	const uid = 920001
	cid, err := m.Create(ctx, uid, "live-bench", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Destroy(ctx, "live-bench", cid); _ = d.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid)) }()

	det, _ := p.ModelDetail(ctx, model)
	window := det.ContextLoaded
	if window == 0 {
		window = 32768
	}
	rctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	tasks := Suite()
	if os.Getenv("SYNTAXIS_LIVE_HARD") == "1" {
		tasks = []Task{hardTask()}
	}
	rep := Run(rctx, Env{Model: model, Window: window, Chat: p.Chat,
		Exec: func(c context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
			return m.Exec(c, "live-bench", cid, e)
		}}, tasks)
	if os.Getenv("SYNTAXIS_LIVE_HARD") == "1" {
		r, _ := m.Exec(ctx, "live-bench", cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", "cd /workspace && find . -maxdepth 3 -not -path './.go*' | head -30; echo ---; cat go.mod 2>&1 | head -5"}, Timeout: 20 * time.Second})
		t.Logf("workspace after run:\n%s", r.Stdout)
	}
	t.Logf("model=%s window=%d pass_rate=%.0f%% tok/s=%.1f ttft=%.0fms tool_fail=%.0f%% peak_prompt=%d", model, window, rep.PassRate*100, rep.TokensPerSec, rep.TTFTMillis, rep.ToolFailRate*100, rep.MaxPromptUsed)
	for _, tr := range rep.Tasks {
		t.Logf("  %-18s passed=%-5v status=%-9s turns=%d calls=%d fails=%d %.1fs  %s", tr.ID, tr.Passed, tr.Status, tr.Turns, tr.ToolCalls, tr.ToolFails, tr.Seconds, tr.Detail)
	}
	if len(rep.Tasks) == 0 {
		t.Fatal("no tasks ran")
	}
}

// hardTask is a realistic mini-project: write code and tests, run them, fix failures.
func hardTask() Task {
	return Task{ID: "go-project", Category: "multi-step",
		Prompt: "In /workspace create a Go module named example.com/slug with slug.go exposing func Slugify(s string) string " +
			"(lowercase, spaces and underscores become single hyphens, drop all characters except a-z, 0-9 and hyphen, trim leading/trailing hyphens) " +
			"and slug_test.go with a table test of at least 4 cases. Run `go test ./...` and make sure it passes. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			r, err := ex(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", "cd /workspace && go test ./... 2>&1 | tail -5"}, Timeout: 3 * time.Minute})
			out := strings.TrimSpace(r.Stdout)
			return err == nil && strings.Contains(out, "ok"), "go test: " + strings.ReplaceAll(out, "\n", " | ")
		}}
}
