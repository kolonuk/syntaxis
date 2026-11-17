// SPDX-License-Identifier: GPL-2.0-or-later

// Package safehttp is the HTTP client for requests the Syntaxis server makes to an address a
// user typed (a watched feed, a self-hosted GitLab or Gitea). Such a request runs with the
// server's network position, so without care a user could make the server fetch the admin app on
// its loopback, the Docker API proxy, the LLM server or anything on the LAN, and read the reply in
// an error message. The client resolves the name itself and checks every address it connects to,
// including after redirects and DNS changes.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/kolonuk/syntaxis/internal/egress"
)

// neverAllowed cannot be opened up by configuration: "this host" and link-local, which includes
// cloud metadata. Loopback is refused unless an allow entry names it explicitly (only tests do:
// inside the container, loopback is where the admin app and internal endpoints listen).
var neverAllowed = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fe80::/10"),
}

// Check reports why a connection to a must be refused ("" = fine). Private and other blocked
// ranges are refused unless allow contains a; neverAllowed ranges are refused regardless.
func Check(a netip.Addr, allow []netip.Prefix) string {
	a = a.Unmap()
	for _, p := range neverAllowed {
		if p.Contains(a) {
			return "an unspecified or link-local address"
		}
	}
	for _, p := range allow {
		if p.Contains(a) {
			return ""
		}
	}
	if blocked, why := egress.IsAlwaysBlocked(a); blocked {
		return why
	}
	return ""
}

// NewClient returns a client that refuses blocked addresses at connection time. allow lists
// private ranges an operator has opened up (for a forge on the LAN); it never opens loopback or
// link-local, and loopback only when an entry names it. Proxies from the environment are ignored
// so the check sees the real address.
func NewClient(timeout time.Duration, allow ...netip.Prefix) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			lastErr := errors.New("no address")
			for _, ip := range ips {
				a, ok := netip.AddrFromSlice(ip.IP)
				if !ok {
					continue
				}
				if why := Check(a, allow); why != "" {
					lastErr = fmt.Errorf("that address is not allowed (%s)", why)
					continue
				}
				c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
		Proxy:                 nil,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-web address refused")
			}
			return nil
		},
	}
}

// CheckBaseURL accepts the address of a service a user typed: http or https, a host, and nothing
// that could change where requests built from it go (no credentials, query or fragment).
func CheckBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("use an address like https://git.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("the address must not contain a user name, password, query or fragment")
	}
	return nil
}

// ParseAllow reads CIDRs (for configuration), ignoring blanks; a bad entry is an error.
func ParseAllow(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			if a, aerr := netip.ParseAddr(s); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				return nil, fmt.Errorf("bad address range %q", s)
			}
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
