// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// realSandbox returns a sandbox manager on the real Docker engine with gVisor, or skips the test
// saying what is missing. Nothing here is faked: these tests start real containers.
func realSandbox(t *testing.T, e *env) *sandbox.Manager {
	t.Helper()
	d, err := sandbox.NewDocker("")
	if err != nil {
		t.Skip("NOT TESTED: no Docker client: " + err.Error())
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip("NOT TESTED: Docker is not reachable: " + err.Error())
	}
	if ok, _ := d.HasRuntime(ctx, "runsc"); !ok {
		t.Skip("NOT TESTED: gVisor (runsc) is not registered with Docker")
	}
	cfg := sandbox.DefaultConfig()
	if img := os.Getenv("SYNTAXIS_TEST_SANDBOX_IMAGE"); img != "" {
		cfg.Image = img // a freshly built image under another tag, so a running instance's image is not replaced
	}
	if ok, _ := d.ImagePresent(ctx, cfg.Image); !ok {
		t.Skip("NOT TESTED: the sandbox image " + cfg.Image + " is not built (docker build -t syntaxis-sandbox:latest images/sandbox)")
	}
	// user ids name the workspace volumes: keep clear of any real instance on this Docker host
	if err := e.st.SetUserIDFloor(context.Background(), 920000+time.Now().UnixNano()%50000); err != nil {
		t.Fatal(err)
	}
	return sandbox.NewManager(d, cfg, nil)
}
