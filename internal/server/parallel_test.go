// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The capacity figures come from the real Docker engine and are checked against what the Docker
// command line says about the same machine.
func TestIntegrationParallelCapacityUsesRealHost(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	u := e.userSession("par@example.com")
	ctx := context.Background()

	ps, _ := e.st.ListProviders(ctx)
	ps[0].Concurrency = 64 // the model is not the limit here, the machine is
	if _, err := e.st.UpsertProvider(ctx, ps[0]); err != nil {
		t.Fatal(err)
	}
	code, c, _ := u.do("GET", "/api/parallel/capacity?model=prov/m1", nil)
	if code != 200 {
		t.Fatalf("capacity: %d %v", code, c)
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.NCPU}}").Output(); err == nil {
		want, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if got := int(c["host_cpus"].(float64)); got != want {
			t.Fatalf("host cpus %d, docker says %d", got, want)
		}
	} else {
		t.Log("NOT CROSS-CHECKED: the docker command is not available to compare against")
	}
	lim := e.app.Sandbox.Cfg.Limits
	hostCPU := int(c["host_cpus"].(float64)) / int(lim.CPUs)
	rec := int(c["recommended"].(float64))
	if rec < 1 || rec > hostCPU || rec > maxAttempts {
		t.Fatalf("recommended %d outside 1..%d: %v", rec, hostCPU, c)
	}

	// a model that serves one request at a time brings the recommendation down to one
	ps[0].Concurrency = 1
	_, _ = e.st.UpsertProvider(ctx, ps[0])
	_, c, _ = u.do("GET", "/api/parallel/capacity?model=prov/m1", nil)
	if int(c["recommended"].(float64)) != 1 {
		t.Fatalf("one-at-a-time model should recommend 1: %v", c)
	}

	// asking for more than is allowed, or for fewer than two, is refused before anything starts
	if code, _, _ := u.do("POST", "/api/parallel", map[string]any{"model": "prov/m1", "count": 1, "prompt": "x"}); code != 400 {
		t.Fatalf("one attempt is not parallel: %d", code)
	}
	if code, _, _ := u.do("POST", "/api/parallel", map[string]any{"model": "prov/m1", "count": maxAttempts + 1, "prompt": "x"}); code != 429 {
		t.Fatalf("too many attempts: %d", code)
	}
}
