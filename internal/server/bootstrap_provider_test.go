// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestBootstrapProviderOnlyOnAnEmptyInstance(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// no address: nothing happens
	e.app.BootstrapProvider(ctx, config.BootstrapProvider{}, quietLog)
	if ps, _ := e.st.ListProviders(ctx); len(ps) != 0 {
		t.Fatalf("a provider appeared without an address: %+v", ps)
	}
	// an unknown kind is refused, not stored
	e.app.BootstrapProvider(ctx, config.BootstrapProvider{URL: "http://llm.internal:11434", Name: "x", Kind: "nope"}, quietLog)
	if ps, _ := e.st.ListProviders(ctx); len(ps) != 0 {
		t.Fatalf("an unknown kind was stored: %+v", ps)
	}
	// a model server that is not there: the provider is added (the admin can discover later) and
	// no model is switched on
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	e.app.BootstrapProvider(short, config.BootstrapProvider{URL: "http://127.0.0.1:1", Name: "local-llm", Kind: "ollama", EnableModels: "all"}, quietLog)
	ps, _ := e.st.ListProviders(ctx)
	if len(ps) != 1 || ps[0].Name != "local-llm" || !ps[0].Enabled || ps[0].Concurrency != 1 {
		t.Fatalf("%+v", ps)
	}
	if ms, _ := e.st.ListModels(ctx, ps[0].ID); len(ms) != 0 {
		t.Fatalf("models from a server that is not there: %+v", ms)
	}
	// a second start never adds another or changes what the admin has done
	e.app.BootstrapProvider(ctx, config.BootstrapProvider{URL: "http://127.0.0.1:2", Name: "other", Kind: "ollama"}, quietLog)
	if ps2, _ := e.st.ListProviders(ctx); len(ps2) != 1 || ps2[0].Name != "local-llm" {
		t.Fatalf("a restart changed the providers: %+v", ps2)
	}
	es, _ := e.st.ListAudit(ctx, 0)
	found := false
	for _, a := range es {
		found = found || a.Action == "provider.bootstrap"
	}
	if !found {
		t.Error("adding the provider was not audited")
	}
}

// Against the real model server (opt-in, like the other live tests).
func TestLiveBootstrapProviderDiscoversAndEnablesModels(t *testing.T) {
	env := testenv.Load(t)
	e := newEnv(t)
	ctx := context.Background()
	e.app.BootstrapProvider(ctx, config.BootstrapProvider{URL: env["OLLAMA_URL"], Name: "local-llm", Kind: env["OLLAMA_KIND"], Concurrency: 2, EnableModels: "all"}, quietLog)
	ps, _ := e.st.ListProviders(ctx)
	if len(ps) != 1 {
		t.Fatalf("%+v", ps)
	}
	ms, _ := e.st.ListModels(ctx, ps[0].ID)
	if len(ms) == 0 {
		t.Fatal("no models discovered from the real server")
	}
	on := 0
	for _, m := range ms {
		embed := strings.Contains(strings.ToLower(m.Name), "embed")
		if m.Enabled == embed {
			t.Errorf("model %s: enabled=%v (an embedding model must stay off, a chat model on)", m.Name, m.Enabled)
		}
		if m.Enabled {
			on++
		}
	}
	if on == 0 {
		t.Error("no chat model was enabled")
	}
}
