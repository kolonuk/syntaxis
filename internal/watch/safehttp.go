// SPDX-License-Identifier: GPL-2.0-or-later

// Package watch checks outside sources for something new (a release, a tag, a feed entry, a changed
// page) so an automation can start when it happens. Every request is made by the Syntaxis server on
// behalf of a user, to an address the user typed, so it is made through a client that refuses
// anything that is not the public internet.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/kolonuk/syntaxis/internal/egress"
)

// MaxBody is how much of a response is read.
const MaxBody = 1 << 20

// NewClient returns an HTTP client for watching public sources. It resolves the name itself and
// refuses private, loopback, link-local, metadata and other blocked addresses at connection time
// (so DNS tricks and redirects cannot reach them either).
func NewClient() *http.Client {
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
				if blocked, why := egress.IsAlwaysBlocked(a.Unmap()); blocked {
					lastErr = fmt.Errorf("that address is not allowed (%s)", why)
					continue
				}
				c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
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
		Proxy:                 nil, // never through a configured proxy: the dial check must see the real address
	}
	return &http.Client{
		Transport: tr,
		Timeout:   25 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

// get fetches a URL (http or https only) and returns at most MaxBody bytes.
func get(ctx context.Context, hc *http.Client, url string, header map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, 0, errors.New("only http and https addresses can be watched")
	}
	if req.URL.User != nil {
		return nil, 0, errors.New("put no password in the address")
	}
	req.Header.Set("User-Agent", "syntaxis-watch")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not fetch it: %w", unwrapURL(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return b, resp.StatusCode, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}
	return b, resp.StatusCode, nil
}

func unwrapURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		return ue.Unwrap()
	}
	return err
}
