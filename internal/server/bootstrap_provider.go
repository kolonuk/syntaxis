// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/store"
)

// BootstrapProvider adds the model provider named in the environment, but only on an instance that
// has none yet, so a rebuilt host comes up ready to use and an admin's later changes (a renamed or
// removed provider) are never undone. It then discovers the models, retrying for a while because the
// model server may still be starting, and switches on the ones asked for.
func (a *App) BootstrapProvider(ctx context.Context, bp config.BootstrapProvider, log *slog.Logger) {
	if bp.URL == "" {
		return
	}
	existing, err := a.Store.ListProviders(ctx)
	if err != nil || len(existing) > 0 {
		return
	}
	if _, err := provider.New(provider.Config{Kind: bp.Kind, BaseURL: bp.URL}); err != nil {
		log.Error("bootstrap provider: not added", "err", err)
		return
	}
	if bp.Concurrency < 1 {
		bp.Concurrency = 1
	}
	id, err := a.Store.UpsertProvider(ctx, store.Provider{Name: bp.Name, Kind: bp.Kind, BaseURL: bp.URL, Concurrency: bp.Concurrency, Enabled: true})
	if err != nil {
		log.Error("bootstrap provider: could not save it", "err", err)
		return
	}
	_, _ = a.Store.AppendAudit(ctx, "bootstrap", "provider.bootstrap", "provider:"+itoa(id), bp.Name+" "+bp.URL)
	log.Info("bootstrap provider added", "name", bp.Name, "url", bp.URL)
	p, err := a.Store.GetProvider(ctx, id)
	if err != nil {
		return
	}
	for attempt := 0; attempt < 18; attempt++ {
		if n, err := a.RefreshModels(ctx, p); err == nil {
			a.enableBootstrapModels(ctx, p.ID, bp.EnableModels, log)
			log.Info("bootstrap provider models discovered", "models", n)
			return
		} else if attempt == 0 {
			log.Warn("bootstrap provider: the model server is not answering yet, will retry", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
	log.Warn("bootstrap provider: gave up discovering models; use Discover models in the admin app")
}

func (a *App) enableBootstrapModels(ctx context.Context, providerID int64, spec string, log *slog.Logger) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	want := map[string]bool{}
	for _, n := range strings.Split(spec, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	ms, err := a.Store.ListModels(ctx, providerID)
	if err != nil {
		return
	}
	for _, m := range ms {
		// "all" means every chat model: embedding models cannot hold a conversation
		if (spec == "all" && !strings.Contains(strings.ToLower(m.Name), "embed")) || want[m.Name] {
			if err := a.Store.SetModelEnabled(ctx, m.ID, true); err == nil {
				log.Info("bootstrap provider: model enabled", "model", m.Name)
			}
		}
	}
}
