// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// A real MCP server (the reference "everything" server from npm) running in its
// own gVisor container, driven over real stdio attach. Skips without Docker,
// runsc, the sandbox image, or internet access from containers.
func TestIntegrationRealMCPServerInGVisor(t *testing.T) {
	d, err := sandbox.NewDocker("")
	if err != nil {
		t.Skip("no docker client")
	}
	defer d.Close()
	ctx := context.Background()
	if d.Ping(ctx) != nil {
		t.Skip("docker not reachable")
	}
	cfg := sandbox.DefaultConfig()
	cfg.Network = "syntaxis-mcp-it" // a plain bridge: no egress rules, so npx can download
	cfg.DNS = []string{"1.1.1.1"}
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("runsc not registered")
	}
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("sandbox image not built")
	}
	if err := d.EnsureNetwork(ctx, cfg.Network); err != nil {
		t.Fatal(err)
	}
	ap := Approval{Name: "io.modelcontextprotocol/everything", Package: Package{RegistryType: "npm", Identifier: "@modelcontextprotocol/server-everything", Version: "2.3.0", Transport: "stdio"}}
	b := NewBridge(d, cfg)
	defer b.Stop(ctx)
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := b.Start(sctx, ap, nil); err != nil {
		t.Skipf("could not start the server (no internet from containers?): %v", err)
	}
	var echo string
	for _, def := range b.Defs() {
		if strings.HasSuffix(def.Name, "__echo") {
			echo = def.Name
		}
	}
	if echo == "" {
		t.Fatalf("echo tool not offered; tools: %+v", b.Defs())
	}
	text, isErr, err := b.Call(sctx, echo, json.RawMessage(`{"message":"hello from syntaxis"}`))
	if err != nil || isErr || !strings.Contains(text, "hello from syntaxis") {
		t.Fatalf("%q %v %v", text, isErr, err)
	}
	t.Logf("real MCP server tools: %d; echo -> %q", len(b.Defs()), text)
	b.Stop(ctx)
	cs, _ := d.List(ctx)
	for _, c := range cs {
		if c.Labels["io.syntaxis.mcp"] != "" {
			t.Fatal("MCP container left behind")
		}
	}
	_ = d.RemoveVolume(ctx, sandbox.WorkspaceVolume(0))
}
