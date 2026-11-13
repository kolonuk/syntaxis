// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/kolonuk/syntaxis/internal/egress"
)

// NetGuard inspects a command (or URL) for attempts to reach things a sandbox
// must never touch: the local network, the host, cloud metadata, the Docker
// socket, or port scanners and LAN reconnaissance tools. It is a fast first
// line of defence that lets the agent tell the model "no" immediately and
// audits the attempt; the egress firewall remains the real boundary.
//
// The sandbox's own loopback (localhost, 127.0.0.0/8, ::1) is allowed: dev
// servers run there.

// reconTools are programs whose purpose is exploring networks.
var reconTools = map[string]string{
	"nmap": "network scanner", "masscan": "network scanner", "zmap": "network scanner", "arp-scan": "LAN scanner",
	"arping": "LAN probe", "netdiscover": "LAN scanner", "nbtscan": "LAN scanner", "fping": "ping sweep",
	"hping3": "packet crafting", "tcpdump": "packet capture", "tshark": "packet capture", "ettercap": "network attack tool",
	"responder": "network attack tool", "nikto": "web scanner", "hydra": "credential attack tool", "medusa": "credential attack tool",
	"masscan-ng": "network scanner", "zgrab": "network scanner",
}

var (
	ipv4Re    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	cidrRe    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}/\d{1,2}\b`)
	urlHostRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://(?:[^/\s@'"]*@)?(\[[0-9a-f:.]+\]|[^/\s:'"?#]+)`)
	bareHost  = regexp.MustCompile(`(?i)\b(?:[a-z0-9\-]+\.)+(?:local|lan|internal|intranet|corp|home|localdomain|home\.arpa)\b`)
	v6Re      = regexp.MustCompile(`\[([0-9a-fA-F:]{3,})\]`)
)

var partialRe = regexp.MustCompile(`\b(?:192\.168|169\.254|172\.(?:1[6-9]|2\d|3[01])|10\.\d{1,3})\.[\d$]`)

// hostTools take a bare host as a positional argument. curl/wget are handled by
// the URL and address checks instead: their positional values are often files.
var hostTools = map[string]bool{"ping": true, "ping6": true, "ssh": true, "scp": true, "sftp": true, "telnet": true, "ftp": true, "traceroute": true, "tracepath": true, "mtr": true, "nslookup": true, "dig": true, "host": true, "nc": true, "ncat": true, "netcat": true}

// userHostTools take user@host arguments where plain words are often options' values (key files).
var userHostTools = map[string]bool{"ssh": true, "scp": true, "sftp": true}

func isNumber(s string) bool { _, err := strconv.Atoi(s); return err == nil }

var blockedNames = []string{"host.docker.internal", "gateway.docker.internal", "metadata.google.internal", "kubernetes.default", "docker.sock"}

// CheckCommand returns a non-empty reason when the command must be blocked.
func CheckCommand(cmd string) string {
	lc := strings.ToLower(cmd)
	if strings.Contains(lc, "docker.sock") || strings.Contains(lc, "/var/run/docker") {
		return "it touches the Docker socket"
	}
	if regexp.MustCompile(`--(?:net|network)(?:=|\s+)host\b`).MatchString(lc) || strings.Contains(lc, "--privileged") {
		return "it asks for host networking or privileges"
	}
	for _, seg := range splitSegments(cmd) {
		f := strings.Fields(seg)
		for i, w := range f {
			base := w
			if j := strings.LastIndex(base, "/"); j >= 0 {
				base = base[j+1:]
			}
			if why, ok := reconTools[base]; ok {
				return base + " is a " + why + " and scanning networks is not allowed"
			}
			// LAN reconnaissance with ordinary tools
			switch base {
			case "arp":
				return "reading the ARP table explores the local network"
			case "ip":
				if i+1 < len(f) && (f[i+1] == "neigh" || f[i+1] == "neighbor" || f[i+1] == "route" || f[i+1] == "r") {
					return "inspecting neighbours/routes explores the local network"
				}
			case "nc", "ncat", "netcat", "socat":
				if hasFlag(f[i+1:], "z") || hasFlag(f[i+1:], "scan") {
					return "port-scanning with " + base + " is not allowed"
				}
			}
		}
	}
	if m := partialRe.FindString(cmd); m != "" {
		return "it builds addresses in a private range (" + m + "…)"
	}
	for _, seg := range splitSegments(cmd) {
		f := strings.Fields(seg)
		if len(f) < 2 || !hostTools[f[0]] {
			continue
		}
		for _, a := range f[1:] {
			if strings.HasPrefix(a, "-") || strings.ContainsAny(a, "=$/%{}'\"+") {
				continue // options, paths, format strings
			}
			h := a
			hasUser := false
			if i := strings.LastIndex(h, "@"); i >= 0 {
				h, hasUser = h[i+1:], true
			}
			if userHostTools[f[0]] && !hasUser && !strings.Contains(h, ".") {
				continue // a bare word after ssh is usually an option value such as a key file
			}
			if h == strings.ToUpper(h) && len(h) <= 5 {
				continue // DNS record types (A, MX, AAAA, ANY...)
			}
			if strings.Contains(h, "://") {
				continue // URLs are checked below
			}
			if i := strings.Index(h, ":"); i > 0 && strings.Count(h, ":") == 1 {
				h = h[:i]
			}
			if why := CheckHost(h); why != "" && !isNumber(h) {
				return "it targets " + h + " (" + why + ")"
			}
		}
	}
	if m := cidrRe.FindString(cmd); m != "" {
		if p, err := netip.ParsePrefix(m); err == nil && p.Bits() < 32 {
			if why := blockedAddr(p.Addr()); why != "" {
				return "it targets " + m + " (" + why + ")"
			}
		}
	}
	for _, m := range ipv4Re.FindAllString(cmd, -1) {
		if a, err := netip.ParseAddr(m); err == nil {
			if why := blockedAddr(a); why != "" {
				return "it targets " + m + " (" + why + ")"
			}
		}
	}
	for _, m := range v6Re.FindAllStringSubmatch(cmd, -1) {
		if a, err := netip.ParseAddr(m[1]); err == nil {
			if why := blockedAddr(a); why != "" {
				return "it targets [" + m[1] + "] (" + why + ")"
			}
		}
	}
	for _, m := range urlHostRe.FindAllStringSubmatch(cmd, -1) {
		if why := CheckHost(m[1]); why != "" {
			return "it targets " + m[1] + " (" + why + ")"
		}
	}
	if m := bareHost.FindString(lc); m != "" {
		return "it targets " + m + " (a local-network hostname)"
	}
	for _, n := range blockedNames {
		if strings.Contains(lc, n) {
			return "it targets " + n
		}
	}
	return ""
}

func hasFlag(args []string, letter string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			if strings.TrimPrefix(a, "--") == letter {
				return true
			}
			continue
		}
		if strings.HasPrefix(a, "-") && strings.Contains(a[1:], letter) {
			return true
		}
	}
	return false
}

// CheckHost classifies a URL host (hostname, IPv4/IPv6 literal, or an obfuscated
// numeric form). It returns a reason when the host must not be reached.
func CheckHost(host string) string {
	h := strings.ToLower(strings.Trim(host, "[]"))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return ""
	}
	switch h {
	case "localhost", "ip6-localhost":
		return "" // the sandbox's own loopback
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return blockedAddr(a)
	}
	if a, ok := numericHost(h); ok {
		if why := blockedAddr(a); why != "" {
			return why + " (written as a numeric/obfuscated address)"
		}
		return ""
	}
	for _, n := range blockedNames {
		if h == n || strings.HasSuffix(h, "."+n) {
			return "an internal service name"
		}
	}
	if !strings.Contains(h, ".") {
		return "a single-label hostname resolves on the local network"
	}
	if bareHost.MatchString(h) {
		return "a local-network hostname"
	}
	return ""
}

// numericHost decodes decimal (2130706433), hex (0x7f000001), octal (0177.0.0.1)
// and mixed-radix IPv4 spellings that resolvers accept.
func numericHost(h string) (netip.Addr, bool) {
	parts := strings.Split(h, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return netip.Addr{}, false
	}
	var nums []uint64
	for _, p := range parts {
		if p == "" {
			return netip.Addr{}, false
		}
		base := 10
		s := p
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			base, s = 16, p[2:]
		case len(p) > 1 && p[0] == '0':
			base, s = 8, p[1:]
		}
		n, err := strconv.ParseUint(s, base, 64)
		if err != nil {
			return netip.Addr{}, false
		}
		nums = append(nums, n)
	}
	var v uint64
	switch len(nums) {
	case 1:
		v = nums[0]
	case 2:
		if nums[0] > 255 || nums[1] > 0xffffff {
			return netip.Addr{}, false
		}
		v = nums[0]<<24 | nums[1]
	case 3:
		if nums[0] > 255 || nums[1] > 255 || nums[2] > 0xffff {
			return netip.Addr{}, false
		}
		v = nums[0]<<24 | nums[1]<<16 | nums[2]
	case 4:
		for _, n := range nums {
			if n > 255 {
				return netip.Addr{}, false
			}
		}
		v = nums[0]<<24 | nums[1]<<16 | nums[2]<<8 | nums[3]
	}
	if v > 0xffffffff {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

// blockedAddr explains why an address is off limits ("" = fine).
func blockedAddr(a netip.Addr) string {
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() {
		return "" // sandbox-local
	}
	if blocked, why := egress.IsAlwaysBlocked(a); blocked {
		return why
	}
	return ""
}
