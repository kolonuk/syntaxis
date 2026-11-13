// SPDX-License-Identifier: GPL-2.0-or-later

// Package egress defines sandbox network policy: which destinations are
// always blocked, how allow/none/open modes behave, and how that translates
// into firewall rules. Rules are generated, not applied, here; applying them
// needs privileges on the Docker host (see docs/security.md).
package egress

import (
	"fmt"
	"net/netip"
	"strings"
)

// Modes.
const (
	ModeOpen      = "open"
	ModeAllowlist = "allowlist"
	ModeNone      = "none"
)

// alwaysBlocked are destination ranges blocked in every mode.
var alwaysBlocked = []struct {
	cidr, why string
}{
	{"10.0.0.0/8", "RFC1918 private network"},
	{"172.16.0.0/12", "RFC1918 private network (includes the Docker bridge)"},
	{"192.168.0.0/16", "RFC1918 private network"},
	{"100.64.0.0/10", "carrier-grade NAT / tailnet range"},
	{"169.254.0.0/16", "link-local and cloud metadata"},
	{"127.0.0.0/8", "loopback"},
	{"0.0.0.0/8", "this network"},
	{"224.0.0.0/4", "multicast"},
	{"240.0.0.0/4", "reserved"},
	{"fc00::/7", "IPv6 unique local"},
	{"fe80::/10", "IPv6 link-local"},
	{"::1/128", "IPv6 loopback"},
	{"fd00:ec2::/32", "AWS IPv6 metadata"},
}

// BlockedSMTPPorts are always blocked outbound to prevent spam.
var BlockedSMTPPorts = []int{25, 465, 587}

// MetadataIPs are cloud metadata endpoints (also covered by link-local).
var MetadataIPs = []string{"169.254.169.254", "169.254.170.2", "fd00:ec2::254"}

// Policy is an effective egress policy for one sandbox.
type Policy struct {
	Mode      string
	Allowlist []string // hostnames or CIDRs, allowlist mode only
	// Extra destinations that must never be reachable: the host's own addresses,
	// the Syntaxis internal address, the LLM server. Hostnames are resolved by
	// the caller before rule generation.
	ExtraBlocked []netip.Prefix
	// Bridge gateway of the sandbox network (always blocked; also inside RFC1918).
	BridgeGateway netip.Addr
}

// Validate checks the mode and allowlist.
func (p Policy) Validate() error {
	switch p.Mode {
	case ModeOpen, ModeNone:
		return nil
	case ModeAllowlist:
		if len(p.Allowlist) == 0 {
			return fmt.Errorf("egress: allowlist mode needs at least one entry")
		}
		for _, e := range p.Allowlist {
			if strings.ContainsAny(e, " \t\r\n;|&$`") || e == "" {
				return fmt.Errorf("egress: invalid allowlist entry %q", e)
			}
		}
		return nil
	}
	return fmt.Errorf("egress: unknown mode %q", p.Mode)
}

// Decision is the verdict for a connection attempt.
type Decision struct {
	Allowed bool
	Reason  string
}

// IsAlwaysBlocked reports whether ip is in a range blocked in every mode.
func IsAlwaysBlocked(ip netip.Addr) (bool, string) {
	ip = ip.Unmap()
	for _, b := range alwaysBlocked {
		if netip.MustParsePrefix(b.cidr).Contains(ip) {
			return true, b.why
		}
	}
	return false, ""
}

// Decide evaluates a connection to host (may be empty) at ip:port.
func (p Policy) Decide(host string, ip netip.Addr, port int) Decision {
	ip = ip.Unmap()
	if blocked, why := IsAlwaysBlocked(ip); blocked {
		return Decision{false, "blocked: " + why}
	}
	for _, x := range p.ExtraBlocked {
		if x.Contains(ip) {
			return Decision{false, "blocked: Syntaxis internal or LLM server address"}
		}
	}
	if p.BridgeGateway.IsValid() && p.BridgeGateway == ip {
		return Decision{false, "blocked: Docker bridge gateway"}
	}
	for _, sp := range BlockedSMTPPorts {
		if port == sp {
			return Decision{false, "blocked: outbound SMTP is not allowed"}
		}
	}
	switch p.Mode {
	case ModeNone:
		return Decision{false, "blocked: network disabled for this sandbox"}
	case ModeAllowlist:
		if p.allowed(host, ip) {
			return Decision{true, "allowed by allowlist"}
		}
		return Decision{false, "blocked: not on the allowlist"}
	case ModeOpen:
		return Decision{true, "allowed: open egress"}
	}
	return Decision{false, "blocked: invalid policy"}
}

func (p Policy) allowed(host string, ip netip.Addr) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range p.Allowlist {
		e = strings.ToLower(e)
		if pfx, err := netip.ParsePrefix(e); err == nil {
			if pfx.Contains(ip) {
				return true
			}
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			if a == ip {
				return true
			}
			continue
		}
		if host == "" {
			continue
		}
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}

// DNSDecision decides whether a DNS name may be resolved for a sandbox. In
// allowlist mode only listed names (and subdomains) resolve; in none mode
// nothing resolves.
func (p Policy) DNSDecision(name string) Decision {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	switch p.Mode {
	case ModeNone:
		return Decision{false, "blocked: network disabled"}
	case ModeAllowlist:
		for _, e := range p.Allowlist {
			e = strings.ToLower(e)
			if _, err := netip.ParsePrefix(e); err == nil {
				continue
			}
			if _, err := netip.ParseAddr(e); err == nil {
				continue
			}
			if name == e || strings.HasSuffix(name, "."+e) {
				return Decision{true, "allowed by allowlist"}
			}
		}
		return Decision{false, "blocked: not on the allowlist"}
	}
	return Decision{true, "allowed"}
}
