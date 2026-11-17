// SPDX-License-Identifier: GPL-2.0-or-later

package safehttp

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestCheckRefusesInternalAddressesUnlessAllowed(t *testing.T) {
	lan := []netip.Prefix{netip.MustParsePrefix("192.168.5.0/24")}
	for addr, wantRefused := range map[string]bool{
		"8.8.8.8": false, "127.0.0.1": true, "::1": true, "169.254.169.254": true, "10.1.2.3": true,
		"172.17.0.2": true, "192.168.5.9": false, "192.168.6.1": true, "0.0.0.0": true, "::ffff:127.0.0.1": true,
	} {
		if refused := Check(netip.MustParseAddr(addr), lan) != ""; refused != wantRefused {
			t.Errorf("%s: refused=%v, want %v", addr, refused, wantRefused)
		}
	}
	// metadata cannot be opened up by configuration
	if Check(netip.MustParseAddr("169.254.169.254"), []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}) == "" {
		t.Fatal("link-local was allowed")
	}
}

// A real listener on loopback: the client must not connect to it, and must not even open a socket.
func TestClientNeverConnectsToARefusedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	_, err = NewClient(3 * time.Second).Do(req)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	select {
	case <-accepted:
		t.Fatal("the client connected to a loopback service")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCheckBaseURLAndParseAllow(t *testing.T) {
	for _, ok := range []string{"https://git.example.com", "http://git.example.com:3000/sub"} {
		if CheckBaseURL(ok) != nil {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "git.example.com", "file:///etc/passwd", "https://u:p@x.example.com", "https://x.example.com/?q", "https://x.example.com/#f", "gopher://x"} {
		if CheckBaseURL(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, err := ParseAllow([]string{"192.0.2.0/24", "", "198.51.100.7"}); err != nil || len(p) != 2 || p[1].Bits() != 32 {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := ParseAllow([]string{"not-a-range"}); err == nil {
		t.Fatal("a bad range was accepted")
	}
}
