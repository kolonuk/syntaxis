// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"net/netip"
	"strings"
	"testing"
)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestAlwaysBlockedInEveryMode(t *testing.T) {
	targets := map[string]string{
		"host lan":      "192.168.1.10",
		"rfc1918 10":    "10.1.2.3",
		"docker bridge": "172.17.0.1",
		"metadata":      "169.254.169.254",
		"ecs metadata":  "169.254.170.2",
		"loopback":      "127.0.0.1",
		"cgnat":         "100.64.1.1",
		"ipv6 ula":      "fd12::1",
		"ipv6 ll":       "fe80::1",
		"v4-mapped lan": "::ffff:192.168.0.5",
	}
	for _, mode := range []string{ModeOpen, ModeAllowlist, ModeNone} {
		p := Policy{Mode: mode, Allowlist: []string{"192.168.0.0/16", "169.254.169.254", "10.0.0.0/8"}}
		for name, addr := range targets {
			if d := p.Decide("", ip(addr), 443); d.Allowed {
				t.Errorf("mode=%s: %s (%s) reachable: %+v", mode, name, addr, d)
			}
		}
	}
}

func TestSMTPBlockedEverywhere(t *testing.T) {
	for _, mode := range []string{ModeOpen, ModeAllowlist} {
		p := Policy{Mode: mode, Allowlist: []string{"mail.example.com"}}
		for _, port := range []int{25, 465, 587} {
			if d := p.Decide("mail.example.com", ip("93.184.216.34"), port); d.Allowed {
				t.Errorf("mode=%s port=%d allowed", mode, port)
			}
		}
	}
	if d := (Policy{Mode: ModeOpen}).Decide("", ip("93.184.216.34"), 443); !d.Allowed {
		t.Fatal("https should be allowed in open mode")
	}
	if d := (Policy{Mode: ModeOpen}).Decide("", ip("93.184.216.34"), 2525); !d.Allowed {
		t.Fatal("only 25/465/587 are blocked")
	}
}

func TestOpenAllowsInternet(t *testing.T) {
	p := Policy{Mode: ModeOpen}
	if d := p.Decide("example.com", ip("93.184.216.34"), 443); !d.Allowed {
		t.Fatalf("%+v", d)
	}
	if d := p.Decide("", ip("2606:2800:220:1:248:1893:25c8:1946"), 443); !d.Allowed {
		t.Fatal("public ipv6")
	}
}

func TestNoneBlocksPublic(t *testing.T) {
	if d := (Policy{Mode: ModeNone}).Decide("example.com", ip("93.184.216.34"), 443); d.Allowed {
		t.Fatal("none mode allowed traffic")
	}
}

func TestAllowlist(t *testing.T) {
	p := Policy{Mode: ModeAllowlist, Allowlist: []string{"github.com", "registry.example.org", "203.0.113.0/24", "198.51.100.7"}}
	cases := []struct {
		host, addr string
		want       bool
	}{
		{"github.com", "140.82.112.3", true},
		{"api.github.com", "140.82.112.5", true},
		{"GitHub.com.", "140.82.112.3", true},
		{"evilgithub.com", "6.6.6.6", false},
		{"github.com.evil.net", "6.6.6.6", false},
		{"", "203.0.113.77", true},
		{"", "198.51.100.7", true},
		{"", "198.51.100.8", false},
		{"other.example.net", "93.184.216.34", false},
		{"", "93.184.216.34", false},
	}
	for _, c := range cases {
		if got := p.Decide(c.host, ip(c.addr), 443).Allowed; got != c.want {
			t.Errorf("%s %s: got %v", c.host, c.addr, got)
		}
	}
}

func TestExtraBlockedAndGateway(t *testing.T) {
	p := Policy{Mode: ModeOpen,
		ExtraBlocked:  []netip.Prefix{netip.MustParsePrefix("203.0.113.5/32")}, // e.g. LLM server's public address
		BridgeGateway: ip("198.51.100.1")}
	if d := p.Decide("llm.internal", ip("203.0.113.5"), 11434); d.Allowed || !strings.Contains(d.Reason, "LLM") {
		t.Fatalf("LLM server reachable directly: %+v", d)
	}
	if d := p.Decide("", ip("198.51.100.1"), 22); d.Allowed {
		t.Fatal("gateway reachable")
	}
}

func TestDNSDecision(t *testing.T) {
	if !(Policy{Mode: ModeOpen}).DNSDecision("anything.example").Allowed {
		t.Fatal("open dns")
	}
	if (Policy{Mode: ModeNone}).DNSDecision("example.com").Allowed {
		t.Fatal("none dns")
	}
	p := Policy{Mode: ModeAllowlist, Allowlist: []string{"github.com", "10.0.0.0/8"}}
	if !p.DNSDecision("API.github.com.").Allowed || p.DNSDecision("evil.com").Allowed || p.DNSDecision("notgithub.com").Allowed {
		t.Fatal("allowlist dns")
	}
}

func TestValidate(t *testing.T) {
	bad := []Policy{{Mode: "wide"}, {Mode: ModeAllowlist}, {Mode: ModeAllowlist, Allowlist: []string{"a.com; rm -rf /"}}, {Mode: ModeAllowlist, Allowlist: []string{""}}}
	for _, p := range bad {
		if p.Validate() == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	for _, p := range []Policy{{Mode: ModeOpen}, {Mode: ModeNone}, {Mode: ModeAllowlist, Allowlist: []string{"a.com"}}} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
}

func TestNftablesOpen(t *testing.T) {
	rs, err := Nftables("syntaxis-sandbox", netip.MustParsePrefix("172.30.0.0/24"), ip("172.30.0.2"), Policy{Mode: ModeOpen, BridgeGateway: ip("172.30.0.1"), ExtraBlocked: []netip.Prefix{netip.MustParsePrefix("203.0.113.5/32")}})
	if err != nil {
		t.Fatal(err)
	}
	txt := rs.Text
	for _, want := range []string{
		"table inet syntaxis_sandbox",
		"ip saddr 172.30.0.0/24 ip daddr 172.30.0.2 udp dport 53 accept",
		"udp dport 53 counter drop",
		"tcp dport { 25, 465, 587 } counter drop",
		"169.254.0.0/16", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "172.30.0.1/32", "203.0.113.5/32",
		"ip saddr 172.30.0.0/24 ip daddr 172.30.0.0/24 counter drop", // sandbox-to-sandbox
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("ruleset missing %q\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "allowed_hosts") || strings.HasSuffix(strings.TrimSpace(txt), "counter drop\n  }") {
		t.Error("open mode must not end in a default drop")
	}
	if !strings.Contains(txt, "ct state established,related accept") || strings.Index(txt, "ct state established,related accept") > strings.Index(txt, "ip daddr 172.30.0.0/24 counter drop") {
		t.Error("established replies must be accepted before the sandbox-to-sandbox drop (DNS answers come from inside the subnet)")
	}
	// resolver accept must come before the generic DNS drop (rule order matters)
	if strings.Index(txt, "udp dport 53 accept") > strings.Index(txt, "udp dport 53 counter drop") {
		t.Error("resolver accept after drop")
	}
}

func TestNftablesNoneAndAllowlist(t *testing.T) {
	sub := netip.MustParsePrefix("172.30.0.0/24")
	rs, _ := Nftables("t", sub, ip("172.30.0.2"), Policy{Mode: ModeNone})
	if !strings.Contains(rs.Text, "ip saddr 172.30.0.0/24 counter drop") {
		t.Fatalf("none mode needs a catch-all drop:\n%s", rs.Text)
	}
	rs, err := Nftables("t", sub, ip("172.30.0.2"), Policy{Mode: ModeAllowlist, Allowlist: []string{"github.com", "203.0.113.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rs.Text, "ip daddr { 203.0.113.0/24 } accept") || !strings.Contains(rs.Text, "@allowed_hosts") || !strings.Contains(rs.Text, "set allowed_hosts") {
		t.Fatalf("allowlist rules:\n%s", rs.Text)
	}
	// the final rule for the subnet in allowlist mode is a drop
	lines := strings.Split(strings.TrimSpace(rs.Text), "\n")
	var last string
	for _, l := range lines {
		if strings.Contains(l, "ip saddr 172.30.0.0/24") {
			last = l
		}
	}
	if !strings.Contains(last, "drop") {
		t.Fatalf("allowlist must end in drop, last rule: %s", last)
	}
	if _, err := Nftables("t", sub, ip("172.30.0.2"), Policy{Mode: "x"}); err == nil {
		t.Fatal("invalid policy")
	}
}

// Traffic to the Docker host's own addresses takes the input hook, not forward. Without
// this chain a sandbox reaches every service listening on the host (SSH, an LLM server).
func TestRulesBlockConnectionsToTheHostItself(t *testing.T) {
	for _, mode := range []string{ModeOpen, ModeNone} {
		rs, err := Nftables("t", netip.MustParsePrefix("172.30.0.0/24"), ip("172.30.0.2"), Policy{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		txt := rs.Text
		i := strings.Index(txt, "hook input")
		if i < 0 {
			t.Fatalf("%s: no input chain:\n%s", mode, txt)
		}
		chain := txt[i:]
		chain = chain[:strings.Index(chain, "}")]
		est := strings.Index(chain, "ip saddr 172.30.0.0/24 ct state established,related accept")
		drop := strings.Index(chain, "ip saddr 172.30.0.0/24 counter drop")
		if est < 0 || drop < 0 || est > drop {
			t.Fatalf("%s: input chain must accept replies, then drop the sandbox subnet:\n%s", mode, chain)
		}
		if r := strings.Index(chain, "ip saddr 172.30.0.2 accept"); r < 0 || r > drop {
			t.Fatalf("%s: the Syntaxis resolver address must be exempt before the drop:\n%s", mode, chain)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	if got := SanitizeName("a b;c-d"); got != "a_b_c_d" {
		t.Fatal(got)
	}
	rs, _ := Nftables("evil; flush ruleset", netip.MustParsePrefix("172.30.0.0/24"), netip.Addr{}, Policy{Mode: ModeOpen})
	if strings.Contains(rs.Text, "flush ruleset") {
		t.Fatal("table name injection")
	}
}

func TestRulesAlsoFilterLayer2SoIsolationDoesNotDependOnBrNetfilter(t *testing.T) {
	sub := netip.MustParsePrefix("172.30.0.0/24")
	rs, err := Nftables("syx", sub, netip.MustParseAddr("172.30.0.2"), Policy{Mode: ModeOpen})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"table bridge syx_l2",
		"type filter hook forward priority -200",
		"ip saddr 172.30.0.2 ip daddr 172.30.0.0/24 accept",
		"ip saddr 172.30.0.0/24 ip daddr 172.30.0.2 accept",
		"ip saddr 172.30.0.0/24 ip daddr 172.30.0.0/24 counter drop",
	} {
		if !strings.Contains(rs.Text, want) {
			t.Errorf("missing %q in\n%s", want, rs.Text)
		}
	}
	// the resolver exceptions must come before the drop
	if strings.Index(rs.Text, "ip daddr 172.30.0.2 accept") > strings.LastIndex(rs.Text, "counter drop\n  }\n}") {
		t.Error("exception after the drop")
	}
}

// The host also runs other applications. Every rule that drops must be limited to traffic
// from the sandbox subnet or the sandbox bridge; nothing may touch anyone else's packets.
func TestRulesOnlyEverDropSandboxTraffic(t *testing.T) {
	for _, mode := range []string{ModeOpen, ModeNone, ModeAllowlist} {
		for _, br := range []string{"", "br-0123456789ab"} {
			pol := Policy{Mode: mode, Allowlist: []string{"203.0.113.0/24"}, BridgeGateway: ip("172.30.0.1"), Bridge: br,
				ExtraBlocked: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::/32")}}
			rs, err := Nftables("t", netip.MustParsePrefix("172.30.0.0/24"), ip("172.30.0.2"), pol)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range strings.Split(rs.Text, "\n") {
				l = strings.TrimSpace(l)
				if !strings.HasSuffix(l, "drop") && !strings.HasSuffix(l, "reject") {
					continue
				}
				if !strings.HasPrefix(l, "ip saddr 172.30.0.0/24 ") && !(br != "" && strings.HasPrefix(l, `iifname "`+br+`" `)) {
					t.Errorf("mode %s bridge %q: rule can affect traffic that is not from a sandbox: %s", mode, br, l)
				}
			}
			for _, chainPolicy := range []string{"policy drop", "policy reject"} {
				if strings.Contains(rs.Text, chainPolicy) {
					t.Errorf("a chain must never default to dropping: %s", chainPolicy)
				}
			}
		}
	}
	if bridgeName(`br0"; flush ruleset; "`) != "" {
		t.Fatal("an unsafe interface name was accepted")
	}
}
