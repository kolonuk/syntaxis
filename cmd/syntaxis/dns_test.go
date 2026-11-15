// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"net/netip"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// A sandbox started after the last periodic refresh must still get its own policy
// on its first DNS query, not be treated as unknown for up to five seconds.
func TestIPMapLookupRefreshesOnMiss(t *testing.T) {
	ctx := context.Background()
	fake := sandbox.NewFake()
	mgr := sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	ips := &ipMap{}
	ips.refresh(ctx, fake, "net") // empty: nothing running yet
	id, err := mgr.Create(ctx, 42, "s1", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	fake.IPs = map[string]string{id: "172.30.0.9"}
	ips.lastRefresh = ips.lastRefresh.Add(-2e9) // the last refresh was a while ago
	uid, sess, ok := ips.lookup(ctx, fake, "net", netip.MustParseAddr("172.30.0.9"))
	if !ok || uid != 42 || sess != "s1" {
		t.Fatalf("lookup after start: %d %q %v", uid, sess, ok)
	}
	// unknown addresses stay unknown and do not refresh more than once a second
	if _, _, ok := ips.lookup(ctx, fake, "net", netip.MustParseAddr("172.30.0.200")); ok {
		t.Fatal("an address that is not a sandbox was mapped")
	}
}
