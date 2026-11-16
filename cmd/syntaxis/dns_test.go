// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// A sandbox started after the last periodic refresh must still get its own policy
// on its first DNS query, not be treated as unknown for up to five seconds. This runs a real
// gVisor sandbox on its own Docker network and reads its real address.
func TestIPMapLookupRefreshesOnMiss(t *testing.T) {
	testenv.GVisor(t)
	ctx := context.Background()
	d, err := sandbox.NewDocker("")
	if err != nil {
		testenv.NotTested(t, "no Docker client: "+err.Error())
	}
	defer d.Close()
	cfg := sandbox.DefaultConfig()
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		testenv.NotTested(t, "the sandbox image "+cfg.Image+" is not built")
	}
	cfg.Network = "syntaxis-test-ipmap"
	if err := d.EnsureNetwork(ctx, cfg.Network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", cfg.Network).Run() })
	mgr := sandbox.NewManager(d, cfg, nil)

	ips := &ipMap{}
	ips.refresh(ctx, d, cfg.Network) // nothing of ours is running yet
	const uid = 940042
	id, err := mgr.Create(ctx, uid, "s1", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Destroy(ctx, "s1", id); _ = d.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid)) })
	ipStr, err := d.ContainerIP(ctx, id, cfg.Network)
	if err != nil || strings.TrimSpace(ipStr) == "" {
		t.Fatalf("the sandbox has no address: %q %v", ipStr, err)
	}
	ips.lastRefresh = ips.lastRefresh.Add(-2e9) // the last refresh was a while ago
	user, sess, ok := ips.lookup(ctx, d, cfg.Network, netip.MustParseAddr(ipStr))
	if !ok || user != uid || sess != "s1" {
		t.Fatalf("lookup after start: %d %q %v", user, sess, ok)
	}
	// an address that is not a sandbox stays unknown
	if _, _, ok := ips.lookup(ctx, d, cfg.Network, netip.MustParseAddr("172.31.255.250")); ok {
		t.Fatal("an address that is not a sandbox was mapped")
	}
}
