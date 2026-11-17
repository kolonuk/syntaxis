// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/queue"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Probe kinds stored in the probes table.
const (
	probeTools     = "tools"
	probeStreaming = "streaming"
)

// probeTable is what is known from live probes, keyed for quick lookup.
type probeTable struct {
	tools    map[string]store.Probe // "providerID/model"
	provider map[int64]store.Probe  // streaming, per provider
}

func (a *App) probes(ctx context.Context) probeTable {
	t := probeTable{tools: map[string]store.Probe{}, provider: map[int64]store.Probe{}}
	ps, _ := a.Store.ListProbes(ctx)
	for _, p := range ps {
		switch p.Kind {
		case probeTools:
			t.tools[fmt.Sprintf("%d/%s", p.ProviderID, p.Model)] = p
		case probeStreaming:
			t.provider[p.ProviderID] = p
		}
	}
	return t
}

// toolsUsable says whether agent mode should be offered for a model. A live probe is the evidence
// when there is one; otherwise what the server says about the model.
func (t probeTable) toolsUsable(m store.Model) bool {
	if p, ok := t.tools[fmt.Sprintf("%d/%s", m.ProviderID, m.Name)]; ok {
		return p.OK
	}
	return m.ToolsSupported != 0
}

// adminModelProbe checks a model for real: does it call a tool when asked, and does the provider's
// stream arrive live. It may load the model, so an admin starts it; it runs in the background at the
// lowest queue priority and records what it found.
func (a *App) adminModelProbe(w http.ResponseWriter, r *http.Request) {
	m, err := a.Store.GetModel(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	prov, err := a.Store.GetProvider(r.Context(), m.ProviderID)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	pr, err := a.ProviderFor(prov)
	if err != nil {
		writeErr(w, 502, "could not connect to the provider")
		return
	}
	if a.Queues == nil {
		a.Queues = NewQueues()
	}
	actor := a.actor(r)
	go func() { //nolint:gosec // the probe must outlive the HTTP request that started it
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		tk, err := a.Queues.For(prov).Enqueue(0, queue.Benchmark)
		if err != nil {
			return
		}
		release, err := tk.Wait(ctx)
		if err != nil {
			return
		}
		defer release()
		record := func(model, kind string, v provider.Verdict, detail string) {
			if v == provider.Inconclusive {
				a.Log.Warn("probe inconclusive", "provider", prov.Name, "model", m.Name, "kind", kind, "why", detail)
				return
			}
			_ = a.Store.SetProbe(ctx, store.Probe{ProviderID: prov.ID, Model: model, Kind: kind, OK: v == provider.Pass, Detail: detail, At: a.Now()})
		}
		v, why := provider.ProbeTools(ctx, pr, m.Name)
		record(m.Name, probeTools, v, why)
		v2, why2 := provider.ProbeStreaming(ctx, pr, m.Name)
		record("", probeStreaming, v2, why2)
		_, _ = a.Store.AppendAudit(ctx, actor, "model.probe", fmt.Sprintf("model:%d", m.ID), fmt.Sprintf("tools=%d streaming=%d", v, v2))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
