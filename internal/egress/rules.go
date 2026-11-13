// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// RuleSet is a generated nftables ruleset for the sandbox network.
type RuleSet struct {
	Table string
	Text  string
}

// SanitizeName reduces a name to a safe nft identifier.
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Nftables generates a ruleset that is applied to traffic FROM the sandbox
// network subnet (forwarded traffic). It is idempotent: the table is
// flushed and rebuilt. The caller applies it with `nft -f -` as root on the
// Docker host. Per-sandbox policy differences (per-user mode) use an
// address-set per policy keyed by container IP.
//
// subnet is the sandbox network CIDR; resolverIP is the Syntaxis DNS resolver
// on that network (the only DNS server sandboxes may use).
func Nftables(table string, subnet netip.Prefix, resolverIP netip.Addr, p Policy) (RuleSet, error) {
	if err := p.Validate(); err != nil {
		return RuleSet{}, err
	}
	table = SanitizeName(table)
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\n", table, table)
	fmt.Fprintf(&b, "table inet %s {\n", table)
	fmt.Fprintf(&b, "  chain egress {\n    type filter hook forward priority -10; policy accept;\n")
	fmt.Fprintf(&b, "    ip saddr != %s accept\n", subnet)
	// the sandbox may talk to the Syntaxis resolver (and only it) for DNS
	if resolverIP.IsValid() {
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s udp dport 53 accept\n", subnet, resolverIP)
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s tcp dport 53 accept\n", subnet, resolverIP)
	}
	// block direct DNS to anywhere else so the resolver cannot be bypassed.
	// Without a Syntaxis resolver this is skipped (sandboxes keep working, but
	// domain policy and DNS logging are unavailable: documented limitation).
	if resolverIP.IsValid() {
		fmt.Fprintf(&b, "    ip saddr %s udp dport 53 counter drop\n", subnet)
		fmt.Fprintf(&b, "    ip saddr %s tcp dport { 53, 853 } counter drop\n", subnet)
	}
	// SMTP out is blocked in every mode
	fmt.Fprintf(&b, "    ip saddr %s tcp dport { %s } counter drop\n", subnet, joinInts(BlockedSMTPPorts))
	// intra-sandbox-network traffic between sandboxes is dropped (isolation between users)
	fmt.Fprintf(&b, "    ip saddr %s ip daddr %s counter drop\n", subnet, subnet)
	// always-blocked destinations
	var v4, v6 []string
	for _, a := range alwaysBlocked {
		pfx := netip.MustParsePrefix(a.cidr)
		if pfx.Addr().Is4() {
			v4 = append(v4, a.cidr)
		} else {
			v6 = append(v6, a.cidr)
		}
	}
	for _, x := range p.ExtraBlocked {
		if x.Addr().Is4() {
			v4 = append(v4, x.String())
		} else {
			v6 = append(v6, x.String())
		}
	}
	if p.BridgeGateway.IsValid() && p.BridgeGateway.Is4() {
		v4 = append(v4, p.BridgeGateway.String()+"/32")
	}
	sort.Strings(v4)
	sort.Strings(v6)
	fmt.Fprintf(&b, "    ip saddr %s ip daddr { %s } counter drop\n", subnet, strings.Join(v4, ", "))
	if len(v6) > 0 {
		fmt.Fprintf(&b, "    ip6 daddr { %s } counter drop\n", strings.Join(v6, ", "))
	}
	// mode-specific tail
	switch p.Mode {
	case ModeNone:
		fmt.Fprintf(&b, "    ip saddr %s counter drop\n", subnet)
	case ModeAllowlist:
		var allow []string
		for _, e := range p.Allowlist {
			if _, err := netip.ParsePrefix(e); err == nil {
				allow = append(allow, e)
			} else if a, err := netip.ParseAddr(e); err == nil {
				allow = append(allow, a.String())
			}
			// hostnames are enforced by the DNS resolver, which only returns
			// addresses for listed names, and by an IP set the resolver feeds
		}
		if len(allow) > 0 {
			fmt.Fprintf(&b, "    ip saddr %s ip daddr { %s } accept\n", subnet, strings.Join(allow, ", "))
		}
		fmt.Fprintf(&b, "    ip saddr %s ip daddr @allowed_hosts accept\n", subnet)
		fmt.Fprintf(&b, "    ip saddr %s counter drop\n", subnet)
	}
	fmt.Fprintf(&b, "  }\n")
	if p.Mode == ModeAllowlist {
		fmt.Fprintf(&b, "  set allowed_hosts {\n    type ipv4_addr\n    flags timeout\n  }\n")
	}
	fmt.Fprintf(&b, "}\n")
	return RuleSet{Table: table, Text: b.String()}, nil
}

func joinInts(ns []int) string {
	var s []string
	for _, n := range ns {
		s = append(s, fmt.Sprint(n))
	}
	return strings.Join(s, ", ")
}
