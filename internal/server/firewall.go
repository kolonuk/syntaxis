// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/egress"
	"github.com/kolonuk/syntaxis/internal/store"
)

// The firewall sync service on the Docker host (scripts/firewall-sync.sh) polls these two
// endpoints on the admin listener, authenticated with SYNTAXIS_FIREWALL_TOKEN:
//
//	GET  /internal/firewall      what the host firewall's sets should contain (see egress.FormatState)
//	POST /internal/firewall/log  kernel log lines for connections the firewall saw
//
// With no token configured both answer 404.

const maxFirewallLogBody = 256 << 10

func (a *App) firewallAuth(w http.ResponseWriter, r *http.Request) bool {
	tok := a.Cfg.FirewallToken
	if tok == "" || a.FirewallState == nil {
		http.NotFound(w, r)
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// FirewallSyncAge is how long ago the host firewall last fetched its state (a large value
// when it never has), for the health check and for waiting until a new sandbox is covered.
func (a *App) FirewallSyncAge() time.Duration {
	a.fwMu.Lock()
	defer a.fwMu.Unlock()
	if a.fwSeen.IsZero() {
		return 365 * 24 * time.Hour
	}
	return a.Now().Sub(a.fwSeen)
}

func (a *App) handleFirewallState(w http.ResponseWriter, r *http.Request) {
	if !a.firewallAuth(w, r) {
		return
	}
	sb, hosts := a.FirewallState(r.Context())
	a.fwMu.Lock()
	a.fwSeen = a.Now()
	a.fwMu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, egress.FormatState(sb, hosts))
}

func (a *App) handleFirewallLog(w http.ResponseWriter, r *http.Request) {
	if !a.firewallAuth(w, r) {
		return
	}
	sb, _ := a.FirewallState(r.Context())
	owner := map[string]egress.SandboxEntry{}
	for _, s := range sb {
		owner[s.IP.String()] = s
	}
	sc := bufio.NewScanner(http.MaxBytesReader(w, r.Body, maxFirewallLogBody))
	sc.Buffer(make([]byte, 0, 4096), 8192)
	n := 0
	for sc.Scan() && n < 500 {
		l, ok := egress.ParseConnLog(sc.Text())
		if !ok {
			continue
		}
		s, known := owner[l.Src.String()]
		if !known {
			continue // not one of our sandboxes any more
		}
		_ = a.Store.AddEgress(r.Context(), store.EgressEntry{Time: a.Now(), UserID: s.User, SessionID: s.Session, DestIP: l.Dst.String(), Port: l.DPort, Protocol: l.Proto, Allowed: l.Allowed})
		n++
	}
	writeJSON(w, http.StatusOK, map[string]int{"recorded": n})
}

// AwaitFirewallFetch blocks (up to max) until the host firewall has fetched its state after
// since, so an address that has just been allowed is in the firewall before the sandbox that
// asked for it tries to connect. It returns at once when the sync service is not running.
func (a *App) AwaitFirewallFetch(since time.Time, max time.Duration) {
	if a.Cfg.FirewallToken == "" || a.FirewallSyncAge() > 15*time.Second {
		return
	}
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		a.fwMu.Lock()
		seen := a.fwSeen
		a.fwMu.Unlock()
		if seen.After(since) {
			time.Sleep(200 * time.Millisecond) // let the script apply what it just fetched
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForFirewall holds a new sandbox for a user with a restricted egress mode until the
// host firewall has picked it up, so it is never briefly open. It does nothing when the sync
// service is not running (the health check reports that) and gives up after a few seconds.
func (a *App) waitForFirewall(ctx context.Context, since time.Time) {
	if a.FirewallState == nil || a.Cfg.FirewallToken == "" || a.FirewallSyncAge() > time.Minute {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		a.fwMu.Lock()
		seen := a.fwSeen
		a.fwMu.Unlock()
		if seen.After(since) {
			time.Sleep(700 * time.Millisecond) // let the script apply what it just fetched
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}
