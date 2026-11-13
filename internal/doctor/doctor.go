// SPDX-License-Identifier: GPL-2.0-or-later

// Package doctor collects a health.State from live systems (database, Docker,
// providers, configuration) for `syntaxis doctor`, the admin Health page and
// periodic checks.
package doctor

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Deps are the live systems to inspect. Any may be nil/zero.
type Deps struct {
	Cfg          *config.Config
	Store        store.Store
	Runtime      sandbox.Runtime
	NewProv      func(p store.Provider) (provider.Provider, error)
	HTTPS        bool // detected on a request
	ProbeTimeout time.Duration
}

// Collect builds the State.
func Collect(ctx context.Context, d Deps) *health.State {
	if d.ProbeTimeout == 0 {
		d.ProbeTimeout = 10 * time.Second
	}
	s := &health.State{}
	collectProviders(ctx, d, s)
	collectSandbox(ctx, d, s)
	collectSecurity(ctx, d, s)
	collectHousekeeping(ctx, d, s)
	return s
}

func collectProviders(ctx context.Context, d Deps, s *health.State) {
	if d.Store == nil {
		return
	}
	provs, _ := d.Store.ListProviders(ctx)
	for _, p := range provs {
		if !p.Enabled {
			continue
		}
		ps := health.ProviderState{Name: p.Name, Kind: p.Kind, Concurrency: p.Concurrency}
		models, _ := d.Store.ListModels(ctx, p.ID)
		pr, err := d.NewProv(p)
		if err != nil {
			ps.Err = err.Error()
			s.Providers = append(s.Providers, ps)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, d.ProbeTimeout)
		lat, err := pr.Ping(cctx)
		switch {
		case err == provider.ErrAuth:
			ps.AuthFailed = true
		case err != nil:
			ps.Err = err.Error()
		default:
			ps.Reachable, ps.Latency = true, lat
			listed, lerr := pr.ListModels(cctx)
			have := map[string]provider.ModelInfo{}
			if lerr == nil {
				for _, m := range listed {
					have[m.Name] = m
				}
			}
			loaded, supported, _ := pr.Loaded(cctx)
			ps.Loaded, ps.LoadedSupported = loaded, supported
			ctxByName := map[string]int{}
			for _, l := range loaded {
				ctxByName[l.Name] = l.ContextSize
			}
			digestGroups := map[string]string{}
			for _, m := range models {
				if _, ok := have[m.Name]; !ok && lerr == nil {
					if m.Enabled {
						ps.MissingEnabled = append(ps.MissingEnabled, m.Name)
					}
					continue
				}
				ms := health.ModelState{Name: m.Name, Enabled: m.Enabled, ContextOverride: m.ContextOverride, Digest: have[m.Name].Digest}
				if det, err := pr.ModelDetail(cctx, m.Name); err == nil {
					ms.ContextAdvert, ms.ContextLoaded, ms.ContextSource = det.ContextAdvert, det.ContextLoaded, det.ContextSource
					ms.ToolsSupported = det.SupportsTools
				}
				if n := ctxByName[m.Name]; n > 0 { // running model: the real window
					ms.ContextLoaded, ms.ContextSource = n, "server (currently loaded)"
				}
				ms.ContextUsed = ms.ContextLoaded
				if ms.ContextOverride > 0 {
					ms.ContextUsed = ms.ContextOverride
				}
				if m.ToolsSupported == 0 {
					f := false
					ms.ToolsSupported = &f
				}
				if ms.Digest != "" {
					ms.AliasOf = digestGroups[ms.Digest]
					if ms.AliasOf == "" {
						digestGroups[ms.Digest] = ms.Digest
						ms.AliasOf = ms.Digest
					}
				}
				ps.Models = append(ps.Models, ms)
			}
		}
		cancel()
		if !ps.Reachable {
			for _, m := range models {
				ps.Models = append(ps.Models, health.ModelState{Name: m.Name, Enabled: m.Enabled})
			}
		}
		s.Providers = append(s.Providers, ps)
	}
}

func collectSandbox(ctx context.Context, d Deps, s *health.State) {
	sb := &s.Sandbox
	if d.Runtime == nil {
		sb.DockerErr = "no Docker connection configured"
		return
	}
	cctx, cancel := context.WithTimeout(ctx, d.ProbeTimeout)
	defer cancel()
	if err := d.Runtime.Ping(cctx); err != nil {
		sb.DockerErr = err.Error()
		return
	}
	sb.DockerReachable = true
	if d.Cfg != nil {
		sb.RunscRegistered, _ = d.Runtime.HasRuntime(cctx, d.Cfg.Docker.Runtime)
		sb.ImagePresent, _ = d.Runtime.ImagePresent(cctx, d.Cfg.Docker.Image)
		sb.SocketProxyUsed = strings.HasPrefix(d.Cfg.Docker.Host, "tcp://")
		sb.SocketProxyReachable = true // Ping succeeded through it
		sb.RawSocketMounted = strings.HasPrefix(d.Cfg.Docker.Host, "unix://") || d.Cfg.Docker.Host == ""
	}
	// Network rules are applied by the operator on the Docker host and cannot be
	// seen through the socket proxy; the sandbox manager probes them at start.
	sb.NetworkRulesApplied = os.Getenv("SYNTAXIS_NETWORK_RULES_APPLIED") == "true"
	if d.Cfg != nil {
		sb.DiskFreePct = diskFreePct(d.Cfg.DataDir)
	}
}

func collectSecurity(ctx context.Context, d Deps, s *health.State) {
	sc := &s.Security
	if d.Cfg == nil {
		return
	}
	c := d.Cfg
	sc.SMTPConfigured = c.SMTP.Host != ""
	sc.MasterKeySet = len(c.MasterKey) == 32
	if kf := os.Getenv("SYNTAXIS_MASTER_KEY_FILE"); kf != "" {
		sc.MasterKeyInDataDir = within(kf, c.DataDir)
	}
	sc.HTTPSDetected = d.HTTPS || strings.HasPrefix(c.BaseURL, "https://")
	sc.AdminListenPublic = isPublicBind(c.AdminListen)
	sc.ScanMode, sc.EgressMode = c.SecretScanMode, c.EgressMode
	sc.ProxyAuthEnabled = c.ProxyAuth.Enabled
	sc.ProxyTrustedEmpty = len(c.ProxyAuth.Trusted) == 0
	for _, t := range c.ProxyAuth.Trusted {
		if t == "0.0.0.0/0" || t == "::/0" {
			sc.ProxyTrustsAll = true
		}
	}
	if d.Store != nil {
		admins, _ := d.Store.ListAccounts(ctx, store.KindAdmin)
		sc.AdminCount = len(admins)
		for _, a := range admins {
			if a.TOTPEnabled {
				sc.AdminsWith2FA++
			}
		}
		days, _ := d.Store.GetSetting(ctx, "password_rotation_days", "30")
		if days == "0" {
			sc.RotationDays = 0
		} else {
			sc.RotationDays = 1
		}
		if m, _ := d.Store.GetSetting(ctx, "secret_scan_mode", ""); m != "" {
			sc.ScanMode = m
		}
		dq, _ := d.Store.GetSetting(ctx, "default_daily_tokens", "0")
		sc.DefaultQuotasSet = dq != "0" && dq != ""
	}
}

func collectHousekeeping(ctx context.Context, d Deps, s *health.State) {
	h := &s.Housekeeping
	if d.Cfg != nil {
		h.VAPIDSet = d.Cfg.VAPIDPublic != "" && d.Cfg.VAPIDPrivate != ""
		h.RetentionSet = d.Cfg.IdleTimeout > 0 && d.Cfg.SandboxMaxAge > 0
		h.PushEnabled = d.Cfg.VAPIDPublic != ""
	}
	if d.Store != nil {
		if v, _ := d.Store.GetSetting(ctx, "last_backup_unix", ""); v != "" {
			var n int64
			for _, ch := range v {
				if ch < '0' || ch > '9' {
					n = 0
					break
				}
				n = n*10 + int64(ch-'0')
			}
			if n > 0 {
				h.LastBackup = time.Unix(n, 0)
			}
		}
	}
}

func within(file, dir string) bool {
	af, e1 := filepath.Abs(file)
	ad, e2 := filepath.Abs(dir)
	if e1 != nil || e2 != nil {
		return false
	}
	rel, err := filepath.Rel(ad, af)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// isPublicBind reports whether a listen address would be reachable from a
// public network (all interfaces or a non-private address).
func isPublicBind(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false // hostname: cannot tell
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
