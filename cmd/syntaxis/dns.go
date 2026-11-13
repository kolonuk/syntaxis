// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/egress"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// ipMap maps sandbox container IPs to their owner and session, refreshed from Docker.
type ipMap struct {
	mu sync.Mutex
	m  map[netip.Addr]struct {
		user    int64
		session string
	}
}

func (i *ipMap) refresh(ctx context.Context, rt sandbox.Runtime, network string) {
	cs, err := rt.List(ctx)
	if err != nil {
		return
	}
	m := map[netip.Addr]struct {
		user    int64
		session string
	}{}
	for _, c := range cs {
		if !c.Running {
			continue
		}
		ipStr, err := rt.ContainerIP(ctx, c.ID, network)
		if err != nil {
			continue
		}
		a, err := netip.ParseAddr(ipStr)
		if err != nil {
			continue
		}
		uid, _ := strconv.ParseInt(c.Labels[sandbox.LabelUser], 10, 64)
		m[a] = struct {
			user    int64
			session string
		}{uid, c.Labels[sandbox.LabelSession]}
	}
	i.mu.Lock()
	i.m = m
	i.mu.Unlock()
}

func upstreamDNS(cfg *config.Config) string {
	if cfg.DNSUpstream != "" {
		return cfg.DNSUpstream
	}
	if f, err := os.Open("/etc/resolv.conf"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if fs := strings.Fields(sc.Text()); len(fs) == 2 && fs[0] == "nameserver" {
				return net.JoinHostPort(fs[1], "53")
			}
		}
	}
	return "1.1.1.1:53"
}

// startDNS runs the sandbox DNS resolver: per-user domain policy, rebinding
// protection, and query logging into the egress log. It also points new
// sandboxes at it. Failure to bind is logged, not fatal; the health check
// reports sandboxes without the resolver as having no DNS policy/logging.
func startDNS(ctx context.Context, cfg *config.Config, st store.Store, mgr *sandbox.Manager, log *slog.Logger) {
	// the Docker socket proxy may still be starting: retry for a while
	var subnet, gw string
	var err error
	for attempt := 0; ; attempt++ {
		if subnet, gw, err = mgr.RT.NetworkInfo(ctx, cfg.Docker.Network); err != nil {
			if eerr := mgr.RT.EnsureNetwork(ctx, cfg.Docker.Network); eerr == nil {
				subnet, gw, err = mgr.RT.NetworkInfo(ctx, cfg.Docker.Network)
			}
		}
		if err == nil {
			break
		}
		if attempt >= 24 || ctx.Err() != nil {
			log.Warn("dns: sandbox network unavailable; sandboxes use the fallback resolver", "fallback", cfg.DNSFallback, "err", err)
			mgr.SetDNS([]string{cfg.DNSFallback})
			return
		}
		time.Sleep(5 * time.Second)
	}
	listen := cfg.DNSListen
	if listen == "" {
		listen = net.JoinHostPort(gw, "53")
	}
	host, _, _ := net.SplitHostPort(listen)
	if ip, err := netip.ParseAddr(host); err == nil {
		mgr.SetDNS([]string{ip.String()})
	}
	var blocked []netip.Prefix
	for _, c := range cfg.EgressBlock {
		if p, err := netip.ParsePrefix(c); err == nil {
			blocked = append(blocked, p)
		}
	}
	gwAddr, _ := netip.ParseAddr(gw)
	ips := &ipMap{}
	ips.refresh(ctx, mgr.RT, cfg.Docker.Network)
	go loop(ctx, 5*time.Second, func() { ips.refresh(ctx, mgr.RT, cfg.Docker.Network) })
	_ = subnet

	r := &egress.Resolver{
		Upstream: upstreamDNS(cfg),
		PolicyFor: func(src netip.Addr) (egress.Policy, bool) {
			ips.mu.Lock()
			info, ok := ips.m[src]
			ips.mu.Unlock()
			if !ok {
				return egress.Policy{}, false
			}
			mode := cfg.EgressMode
			if u, err := st.GetUser(ctx, info.user); err == nil && u.EgressMode != "" {
				mode = u.EgressMode
			}
			return egress.Policy{Mode: mode, Allowlist: cfg.EgressAllow, ExtraBlocked: blocked, BridgeGateway: gwAddr}, true
		},
		Log: func(e egress.DNSLogEntry) {
			ips.mu.Lock()
			info := ips.m[e.Source]
			ips.mu.Unlock()
			_ = st.AddEgress(ctx, store.EgressEntry{Time: e.Time, UserID: info.user, SessionID: info.session, DestHost: strings.TrimSuffix(e.Name, "."), Port: 53, Protocol: "dns", Allowed: e.Allowed, DNS: true})
		},
	}
	go func() {
		if err := r.Serve(ctx, listen); err != nil {
			fb := cfg.DNSFallback
			log.Warn("sandbox DNS resolver stopped; new sandboxes use the fallback resolver (no domain policy or DNS logging)", "listen", listen, "fallback", fb, "err", err)
			mgr.SetDNS([]string{fb})
		}
	}()
	log.Info("sandbox DNS resolver", "listen", listen, "upstream", r.Upstream)
}
