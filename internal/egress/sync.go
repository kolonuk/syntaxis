// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is the contract between Syntaxis and the firewall sync service that runs on the
// Docker host (scripts/firewall-sync.sh). The firewall rules are static; what changes while
// Syntaxis runs is which sandbox addresses are in which mode and which addresses an
// allowlisted hostname currently resolves to. Syntaxis describes that as plain lines, and the
// host script (which has the privileges) turns it into nft set updates after validating every
// line, so Syntaxis never sends the host a command.

// SetToken maps an egress mode to the sandbox set it belongs to.
func SetToken(mode string) string {
	switch mode {
	case ModeNone:
		return "none"
	case ModeAllowlist:
		return "allow"
	}
	return "open"
}

// SandboxEntry is one running sandbox.
type SandboxEntry struct {
	IP      netip.Addr
	User    int64
	Session string
	Mode    string
}

// HostEntry is an address an allowlisted hostname resolved to, with how long to keep it.
type HostEntry struct {
	IP  netip.Addr
	TTL time.Duration
}

// FormatState renders the state for the sync script, one entry per line:
//
//	sandbox <ipv4> none|allow|open
//	host <ipv4> <ttl-seconds>
func FormatState(sandboxes []SandboxEntry, hosts []HostEntry) string {
	var b strings.Builder
	sort.Slice(sandboxes, func(i, j int) bool { return sandboxes[i].IP.Less(sandboxes[j].IP) })
	for _, s := range sandboxes {
		if s.IP.Is4() {
			fmt.Fprintf(&b, "sandbox %s %s\n", s.IP, SetToken(s.Mode))
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].IP.Less(hosts[j].IP) })
	for _, h := range hosts {
		if h.IP.Is4() && h.TTL > 0 {
			fmt.Fprintf(&b, "host %s %d\n", h.IP, int(h.TTL.Seconds()))
		}
	}
	return b.String()
}

// HostTracker remembers the addresses that allowlisted names resolved to.
type HostTracker struct {
	mu sync.Mutex
	m  map[netip.Addr]time.Time
}

// HostTTL is how long a resolved address stays allowed after its last answer.
const HostTTL = 10 * time.Minute

// Add records resolver answers and reports whether any address was not already allowed, which
// is when the host firewall has to be told before the sandbox connects.
func (t *HostTracker) Add(ips []netip.Addr, now time.Time) (fresh bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[netip.Addr]time.Time{}
	}
	for _, ip := range ips {
		if ip.Is4() {
			ip = ip.Unmap()
			// an address whose entry is about to lapse counts as new, so it is refreshed in time
			if until, ok := t.m[ip]; !ok || until.Sub(now) < HostTTL/2 {
				fresh = true
			}
			t.m[ip] = now.Add(HostTTL)
		}
	}
	return fresh
}

// Snapshot returns the live entries and forgets expired ones.
func (t *HostTracker) Snapshot(now time.Time) []HostEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []HostEntry
	for ip, until := range t.m {
		if !until.After(now) {
			delete(t.m, ip)
			continue
		}
		out = append(out, HostEntry{IP: ip, TTL: until.Sub(now)})
	}
	return out
}

// ConnLog is one connection recorded by the firewall's log rules.
type ConnLog struct {
	Allowed bool
	Src     netip.Addr
	Dst     netip.Addr
	Proto   string
	DPort   int
}

var connLogRe = regexp.MustCompile(`syx([ad]): .*?SRC=(\d+\.\d+\.\d+\.\d+) DST=(\d+\.\d+\.\d+\.\d+).*?PROTO=(\w+)(?:.*?DPT=(\d+))?`)

// ParseConnLog reads one kernel log line written by the firewall's log rules.
func ParseConnLog(line string) (ConnLog, bool) {
	m := connLogRe.FindStringSubmatch(line)
	if m == nil {
		return ConnLog{}, false
	}
	src, err1 := netip.ParseAddr(m[2])
	dst, err2 := netip.ParseAddr(m[3])
	if err1 != nil || err2 != nil {
		return ConnLog{}, false
	}
	port, _ := strconv.Atoi(m[5])
	return ConnLog{Allowed: m[1] == "a", Src: src, Dst: dst, Proto: strings.ToLower(m[4]), DPort: port}, true
}
