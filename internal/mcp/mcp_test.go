// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

var ctx = context.Background()

// registryEntry and remoteEntry are catalogue entries (data, not a server) in the shape the real
// registry returns, used to check the review and approval rules.
func registryEntry() Entry {
	return Entry{Name: "com.example/fs", Description: "Filesystem ops", Version: "0.1.5", Repository: "https://github.com/example/mcp", Status: "active",
		Packages: []Package{{RegistryType: "npm", Identifier: "example-fs-mcp", Version: "0.1.5", RuntimeHint: "npx", Transport: "stdio",
			Env: []EnvVar{{Name: "BUCKET", Required: true, Description: "bucket"}, {Name: "API_KEY", Secret: true, Description: "key"}, {Name: "MODE", Default: "ro"}}}}}
}

func remoteEntry() Entry {
	return Entry{Name: "com.example/hosted", Description: "Hosted only", Version: "1.0.0", Remotes: []string{"https://mcp.example.com"}}
}

func TestRealRegistrySearch(t *testing.T) {
	testenv.Network(t, "registry.modelcontextprotocol.io:443")
	r := &Registry{BaseURL: DefaultRegistry}
	es, _, err := r.Search(ctx, "github", "", 20)
	if err != nil || len(es) == 0 {
		t.Fatalf("%v %d", err, len(es))
	}
	found := false
	for _, e := range es {
		if e.Name == "" || e.Version == "" {
			t.Fatalf("incomplete entry %+v", e)
		}
		if e.Name == "io.github.github/github-mcp-server" {
			found = true
			if len(e.Packages) == 0 && len(e.Remotes) == 0 {
				t.Fatalf("no way to run it: %+v", e)
			}
		}
	}
	if !found {
		t.Log("the GitHub server was not on the first page of results; the shape checks above still ran")
	}
}

func TestRegistryRefusesAnswersThatAreNotTheRegistry(t *testing.T) {
	testenv.Network(t, "example.com:443")
	// a real web server that is not an MCP registry
	if _, _, err := (&Registry{BaseURL: "https://example.com"}).Search(ctx, "", "", 0); err == nil {
		t.Fatal("a page that is not the registry was accepted")
	}
}

func TestLocalSource(t *testing.T) {
	l := Local{Entries: []Entry{{Name: "a/docs", Description: "docs search"}, {Name: "b/dns", Description: "dns tools"}}}
	es, _, _ := l.Search(ctx, "dns", "", 0)
	if len(es) != 1 || es[0].Name != "b/dns" {
		t.Fatalf("%+v", es)
	}
}

func entries(t *testing.T) []Entry { return []Entry{registryEntry(), remoteEntry()} }

func TestReviewShowsCredentialsAndCapabilities(t *testing.T) {
	es := entries(t)
	rv, err := Summarise(es[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if !rv.Pinnable || strings.Join(rv.NeedsCredential, ",") != "API_KEY,BUCKET" || len(rv.Capabilities) < 3 || len(rv.Warnings) != 0 {
		t.Fatalf("%+v", rv)
	}
	if !strings.Contains(strings.Join(rv.Capabilities, " "), "untrusted") {
		t.Fatal("must say MCP output is untrusted")
	}
	if _, err := Summarise(es[1], -1); err == nil {
		t.Fatal("remote-only server must not be approvable")
	}
}

func TestReviewWarnings(t *testing.T) {
	e := Entry{Name: "x", Status: "deprecated", Packages: []Package{{RegistryType: "npm", Identifier: "x", Version: "latest", Transport: "sse"}, {RegistryType: "nuget", Identifier: "y", Version: "1.0.0", Transport: "stdio"}}}
	rv, _ := Summarise(e, 0)
	w := strings.Join(rv.Warnings, "|")
	for _, want := range []string{"stdio", "pinned", "deprecated"} {
		if !strings.Contains(w, want) {
			t.Errorf("missing warning %q in %s", want, w)
		}
	}
	rv2, _ := Summarise(e, 1)
	if !strings.Contains(strings.Join(rv2.Warnings, "|"), "not supported") {
		t.Fatal("unsupported type")
	}
}

func newApprovals(t *testing.T) *Approvals {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Approvals{Store: st}
}

func TestApprovalRules(t *testing.T) {
	a := newApprovals(t)
	e := entries(t)[0]
	// required credential not mapped
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"API_KEY": "S1"}); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("unmapped required variable accepted: %v", err)
	}
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"BUCKET": "MCP_BUCKET", "API_KEY": "MCP_KEY"}); err != nil {
		t.Fatal(err)
	}
	// unpinned and non-stdio are refused
	un := Entry{Name: "u", Packages: []Package{{RegistryType: "npm", Identifier: "u", Version: "^1.0.0", Transport: "stdio"}}}
	if err := a.Approve(ctx, "admin:1", un, 0, nil); !errors.Is(err, ErrNotApprovable) {
		t.Fatal("unpinned accepted")
	}
	if err := a.Approve(ctx, "admin:1", entries(t)[1], 0, nil); !errors.Is(err, ErrNotApprovable) {
		t.Fatal("remote accepted")
	}
	list, _ := a.List(ctx)
	if len(list) != 1 || !list[0].Enabled || list[0].ApprovedBy != "admin:1" {
		t.Fatalf("%+v", list)
	}
	es, _ := a.Store.ListAudit(ctx, 0)
	if len(es) != 1 || es[0].Action != "mcp.approve" {
		t.Fatalf("approval must be audited: %+v", es)
	}
	if err := a.Revoke(ctx, "admin:1", e.Name); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(ctx, "admin:1", e.Name); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("double revoke")
	}
}

func TestContainerSpecIsolation(t *testing.T) {
	e := entries(t)[0]
	ap := Approval{Name: e.Name, Package: e.Packages[0], EnvSecrets: map[string]string{"BUCKET": "MCP_BUCKET", "API_KEY": "MCP_KEY"}}
	spec, err := ContainerSpec(sandbox.DefaultConfig(), ap, map[string]string{"MCP_BUCKET": "b1", "MCP_KEY": "k1", "UNRELATED_SECRET": "must-not-appear"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Runtime != "runsc" || spec.Privileged || !spec.ReadOnlyRootFS || len(spec.Mounts) != 0 { // DefaultConfig has no DNS, so no mounts at all
		t.Fatalf("%+v", spec)
	}
	if strings.Join(spec.Cmd, " ") != "npx --yes example-fs-mcp@0.1.5" {
		t.Fatalf("version not pinned in the command: %v", spec.Cmd)
	}
	env := strings.Join(spec.Env, " ")
	if !strings.Contains(env, "BUCKET=b1") || !strings.Contains(env, "API_KEY=k1") || strings.Contains(env, "must-not-appear") || strings.Contains(env, "UNRELATED") {
		t.Fatalf("only mapped secrets may be passed: %s", env)
	}
	// a secret that does not exist fails closed
	if _, err := ContainerSpec(sandbox.DefaultConfig(), ap, nil); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestContainerSpecRejectsInjectionAndUnpinned(t *testing.T) {
	cfg := sandbox.DefaultConfig()
	for name, p := range map[string]Package{
		"shell metachar": {RegistryType: "npm", Identifier: "x; rm -rf /", Version: "1.0.0"},
		"flag":           {RegistryType: "npm", Identifier: "--registry=http://evil", Version: "1.0.0"},
		"unpinned":       {RegistryType: "npm", Identifier: "x", Version: "latest"},
		"bad type":       {RegistryType: "nuget", Identifier: "x", Version: "1.0.0"},
	} {
		if _, err := ContainerSpec(cfg, Approval{Name: "n", Package: p}, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
