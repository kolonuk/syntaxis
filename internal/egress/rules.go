// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"fmt"
	"net/netip"
	"regexp"
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
	// logNew records new connections from sandboxes (rate-limited) in the kernel log, where the
	// firewall sync service picks them up for the egress log. "syxa" = allowed, "syxd" = denied.
	logNew := func(match, prefix string) {
		fmt.Fprintf(&b, "    %s ct state new limit rate 20/second burst 40 packets log prefix \"%s \"\n", match, prefix)
	}
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\n", table, table)
	fmt.Fprintf(&b, "table inet %s {\n", table)
	fmt.Fprintf(&b, "  chain egress {\n    type filter hook forward priority -10; policy accept;\n")
	fmt.Fprintf(&b, "    ip saddr != %s accept\n", subnet)
	// replies to connections a sandbox opened (for example the resolver's DNS answers, which come
	// from an address inside the subnet) must pass; new sandbox-to-sandbox connections are dropped below
	fmt.Fprintf(&b, "    ct state established,related accept\n")
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
	logNew(fmt.Sprintf("ip saddr %s tcp dport { %s }", subnet, joinInts(BlockedSMTPPorts)), "syxd:")
	fmt.Fprintf(&b, "    ip saddr %s tcp dport { %s } counter drop\n", subnet, joinInts(BlockedSMTPPorts))
	// intra-sandbox-network traffic between sandboxes is dropped (isolation between users);
	// the Syntaxis container itself (the resolver address) may open connections to sandboxes
	// for the dev-server preview
	if resolverIP.IsValid() {
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s accept\n", resolverIP, subnet)
	}
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
	logNew(fmt.Sprintf("ip saddr %s ip daddr { %s }", subnet, strings.Join(v4, ", ")), "syxd:")
	fmt.Fprintf(&b, "    ip saddr %s ip daddr { %s } counter drop\n", subnet, strings.Join(v4, ", "))
	// IPv6: the sandbox network has none, and an IPv6 rule cannot be limited by the IPv4
	// subnet, so it is tied to the sandbox bridge or left out. (An unqualified ip6 drop would
	// hit every forwarded IPv6 packet on the host, including other applications'.)
	_ = v6
	if br := bridgeName(p.Bridge); br != "" {
		fmt.Fprintf(&b, "    iifname %q meta nfproto ipv6 counter drop\n", br)
	}
	// Per-sandbox overrides. The firewall sync service keeps these sets in step with the running
	// containers, so a user's own egress mode is enforced here and not only by DNS. Anything not
	// listed falls through to the instance-wide mode below.
	// Each rule is also tied to the sandbox subnet, so a wrong set entry can never affect other traffic.
	per := func(set, rest string) string { return fmt.Sprintf("ip saddr %s ip saddr @%s%s", subnet, set, rest) }
	logNew(per("sandbox_none", ""), "syxd:")
	fmt.Fprintf(&b, "    %s counter drop\n", per("sandbox_none", ""))
	logNew(per("sandbox_allow", " ip daddr @allowed_hosts"), "syxa:")
	fmt.Fprintf(&b, "    %s accept\n", per("sandbox_allow", " ip daddr @allowed_hosts"))
	logNew(per("sandbox_allow", " ip daddr @allow_nets"), "syxa:")
	fmt.Fprintf(&b, "    %s accept\n", per("sandbox_allow", " ip daddr @allow_nets"))
	logNew(per("sandbox_allow", ""), "syxd:")
	fmt.Fprintf(&b, "    %s counter drop\n", per("sandbox_allow", ""))
	logNew(per("sandbox_open", ""), "syxa:")
	fmt.Fprintf(&b, "    %s accept\n", per("sandbox_open", ""))
	// instance-wide mode
	var allow []string
	for _, e := range p.Allowlist {
		if _, err := netip.ParsePrefix(e); err == nil {
			allow = append(allow, e)
		} else if a, err := netip.ParseAddr(e); err == nil {
			allow = append(allow, a.String())
		}
		// hostnames are enforced by the DNS resolver, which only returns addresses for listed
		// names, and by the allowed_hosts set that the firewall sync fills from its answers
	}
	switch p.Mode {
	case ModeNone:
		logNew(fmt.Sprintf("ip saddr %s", subnet), "syxd:")
		fmt.Fprintf(&b, "    ip saddr %s counter drop\n", subnet)
	case ModeAllowlist:
		if len(allow) > 0 {
			logNew(fmt.Sprintf("ip saddr %s ip daddr { %s }", subnet, strings.Join(allow, ", ")), "syxa:")
			fmt.Fprintf(&b, "    ip saddr %s ip daddr { %s } accept\n", subnet, strings.Join(allow, ", "))
		}
		logNew(fmt.Sprintf("ip saddr %s ip daddr @allowed_hosts", subnet), "syxa:")
		fmt.Fprintf(&b, "    ip saddr %s ip daddr @allowed_hosts accept\n", subnet)
		logNew(fmt.Sprintf("ip saddr %s", subnet), "syxd:")
		fmt.Fprintf(&b, "    ip saddr %s counter drop\n", subnet)
	default:
		logNew(fmt.Sprintf("ip saddr %s", subnet), "syxa:")
	}
	fmt.Fprintf(&b, "  }\n")
	// Connections addressed to the Docker host itself (the bridge gateway, the host's LAN
	// address, anything listening on all interfaces such as an LLM server or SSH) are
	// delivered locally and never pass the forward hook, so they need their own chain.
	// Only replies to connections the host opened may come back from a sandbox. The
	// Syntaxis container (the resolver address) is exempt: it is trusted and may share
	// the subnet.
	fmt.Fprintf(&b, "  chain host {\n    type filter hook input priority -10; policy accept;\n")
	if resolverIP.IsValid() {
		fmt.Fprintf(&b, "    ip saddr %s accept\n", resolverIP)
	}
	fmt.Fprintf(&b, "    ip saddr %s ct state established,related accept\n", subnet)
	if br := bridgeName(p.Bridge); br != "" {
		fmt.Fprintf(&b, "    iifname %q meta nfproto ipv6 counter drop\n", br)
	}
	fmt.Fprintf(&b, "    ip saddr %s counter drop\n  }\n", subnet)
	for _, set := range []string{"sandbox_none", "sandbox_allow", "sandbox_open"} {
		fmt.Fprintf(&b, "  set %s {\n    type ipv4_addr\n  }\n", set)
	}
	fmt.Fprintf(&b, "  set allowed_hosts {\n    type ipv4_addr\n    flags timeout\n  }\n")
	fmt.Fprintf(&b, "  set allow_nets {\n    type ipv4_addr\n    flags interval\n")
	if len(allow) > 0 {
		fmt.Fprintf(&b, "    elements = { %s }\n", strings.Join(allow, ", "))
	}
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "}\n")
	// Traffic between two sandboxes on the same bridge is switched at layer 2 and only reaches
	// the forward hook above when the br_netfilter kernel module is loaded and enabled, which
	// not every host does. A bridge-family table filters it regardless, so isolation between
	// sandboxes never depends on that setting.
	l2 := table + "_l2"
	fmt.Fprintf(&b, "table bridge %s\ndelete table bridge %s\n", l2, l2)
	fmt.Fprintf(&b, "table bridge %s {\n  chain forward {\n    type filter hook forward priority -200; policy accept;\n", l2)
	if resolverIP.IsValid() {
		// The Syntaxis container may open connections to sandboxes. A sandbox may only ask it for
		// DNS; it must not reach the app's own listeners (the chat and admin ports share this
		// address), so a new TCP connection to anything but port 53 is dropped. Packets of
		// connections the server opened itself (replies, and the SYN-ACK that starts them) still
		// pass: they are not a bare SYN, which needs no connection tracking to tell.
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s accept\n", resolverIP, subnet)
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s udp dport 53 accept\n", subnet, resolverIP)
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s tcp dport 53 accept\n", subnet, resolverIP)
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s tcp flags & (syn | ack) == syn counter drop\n", subnet, resolverIP)
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s ip protocol tcp accept\n", subnet, resolverIP)
		// path MTU discovery and errors for connections the server opened
		fmt.Fprintf(&b, "    ip saddr %s ip daddr %s ip protocol icmp accept\n", subnet, resolverIP)
	}
	fmt.Fprintf(&b, "    ip saddr %s ip daddr %s counter drop\n  }\n}\n", subnet, subnet)
	return RuleSet{Table: table, Text: b.String()}, nil
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// bridgeName returns a valid interface name or "" (never interpolate anything else into nft).
func bridgeName(s string) string {
	if ifaceRe.MatchString(s) {
		return s
	}
	return ""
}

func joinInts(ns []int) string {
	var s []string
	for _, n := range ns {
		s = append(s, fmt.Sprint(n))
	}
	return strings.Join(s, ", ")
}
