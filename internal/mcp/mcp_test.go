// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"context"
	"encoding/json"
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
	// a name that cannot be a credential, and a variable the package does not have, are refused
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"API_KEY": "has space"}); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("bad credential name accepted: %v", err)
	}
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"not a variable": "X"}); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("a bad variable name was accepted: %v", err)
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
	spec, err := ContainerSpec(sandbox.DefaultConfig(), ap, map[string]string{"MCP_BUCKET": "b1", "MCP_KEY": "k1", "UNRELATED_SECRET": "must-not-appear"}, Owner{UserID: 7})
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
	// the server runs as its user: their id is on the container (so the resolver and the host
	// firewall apply their egress mode), and a user without network gets a server without one
	if spec.Labels[sandbox.LabelUser] != "7" || spec.NetworkDisabled {
		t.Fatalf("an MCP server must carry its user and their network setting: %v %v", spec.Labels, spec.NetworkDisabled)
	}
	if off, err := ContainerSpec(sandbox.DefaultConfig(), ap, map[string]string{"MCP_BUCKET": "b1", "MCP_KEY": "k1"}, Owner{UserID: 7, NoNetwork: true}); err != nil || !off.NetworkDisabled {
		t.Fatalf("a user with egress none must get an MCP server with no network: %v", err)
	}
	// a secret that does not exist fails closed
	if _, err := ContainerSpec(sandbox.DefaultConfig(), ap, nil, Owner{UserID: 7}); err == nil {
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
		if _, err := ContainerSpec(cfg, Approval{Name: "n", Package: p}, nil, Owner{UserID: 7}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A container image says its version in the tag of its reference and leaves the registry's version
// field empty. The official GitHub server is published that way, so it could never be approved.
func TestImageServerIsPinnedByTheVersionInItsTag(t *testing.T) {
	oci := func(ident, version string) Entry {
		return Entry{Name: "io.github.example/img", Repository: "https://example.com/r", Packages: []Package{{RegistryType: "oci", Identifier: ident, Version: version, Transport: "stdio"}}}
	}
	for _, tc := range []struct {
		ident, version string
		pinned         bool
	}{
		{"ghcr.io/github/github-mcp-server:2.2.0", "", true},
		{"ghcr.io/example/app:v1.4.0-rc.1", "", true},
		{"registry.example.com:5000/team/app:3.1.4", "", true}, // a port in the host is not a tag
		{"ghcr.io/example/app@sha256:" + strings.Repeat("a", 64), "", true},
		{"ghcr.io/example/app:latest", "", false},
		{"ghcr.io/example/app:1", "", false},
		{"ghcr.io/example/app", "", false},
		{"registry.example.com:5000/team/app", "", false},
		{"ghcr.io/example/app:latest", "2.0.0", true}, // an explicit exact version still counts
	} {
		rv, err := Summarise(oci(tc.ident, tc.version), 0)
		if err != nil {
			t.Fatal(err)
		}
		if rv.Pinnable != tc.pinned {
			t.Errorf("%s (version %q): pinnable %v, want %v", tc.ident, tc.version, rv.Pinnable, tc.pinned)
		}
		if rv.PullCommand != "docker pull "+tc.ident {
			t.Errorf("a container image needs the command that puts it on the host: %q", rv.PullCommand)
		}
	}
	rv, _ := Summarise(oci("ghcr.io/github/github-mcp-server:2.2.0", ""), 0)
	if !strings.Contains(strings.Join(rv.Warnings, "|"), "tag") || !strings.Contains(strings.Join(rv.Capabilities, "|"), "Docker host") {
		t.Fatalf("a tag pin is weaker than a digest and the image must be on the host; the review must say both: %+v", rv)
	}
	rv, _ = Summarise(oci("ghcr.io/example/app@sha256:"+strings.Repeat("b", 64), ""), 0)
	if strings.Contains(strings.Join(rv.Warnings, "|"), "tag") {
		t.Fatalf("a digest pin needs no warning about tags: %+v", rv)
	}
	if rv, _ := Summarise(oci("ghcr.io/example/app:latest", ""), 0); !strings.Contains(strings.Join(rv.Warnings, "|"), "pinned") {
		t.Fatalf("an unpinned image must be warned about: %+v", rv)
	}
}

func TestImageServerCanBeApprovedAndRunsFromItsTag(t *testing.T) {
	a := newApprovals(t)
	e := Entry{Name: "io.github.example/img", Packages: []Package{{RegistryType: "oci", Identifier: "ghcr.io/example/app:1.2.3", Transport: "stdio"}}}
	if err := a.Approve(ctx, "admin:1", e, 0, nil); err != nil {
		t.Fatalf("an image pinned by tag must be approvable: %v", err)
	}
	list, _ := a.List(ctx)
	if len(list) != 1 || list[0].Package.Version != "1.2.3" || list[0].Package.Ref() != "ghcr.io/example/app:1.2.3" {
		t.Fatalf("%+v", list)
	}
	spec, err := ContainerSpec(sandbox.DefaultConfig(), list[0], nil, Owner{UserID: 910001})
	if err != nil || spec.Image != "ghcr.io/example/app:1.2.3" {
		t.Fatalf("the container runs the image the admin approved: %v %+v", err, spec.Image)
	}
	// an image on a moving tag is refused at approval and again at run time
	moving := Entry{Name: "io.github.example/moving", Packages: []Package{{RegistryType: "oci", Identifier: "ghcr.io/example/app:latest", Transport: "stdio"}}}
	if err := a.Approve(ctx, "admin:1", moving, 0, nil); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("a moving tag was approved: %v", err)
	}
	if _, err := ContainerSpec(sandbox.DefaultConfig(), Approval{Name: "x", Package: moving.Packages[0]}, nil, Owner{UserID: 910001}); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("a moving tag was run: %v", err)
	}
}

// A required variable the admin leaves unmapped takes the credential of its own name: every credential
// is a name in the running user's vault, and there is no instance-wide value to fall back on.
func TestUnmappedRequiredVariableTakesItsOwnName(t *testing.T) {
	a := newApprovals(t)
	e := entries(t)[0]
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"API_KEY": "MY_KEY"}); err != nil {
		t.Fatal(err)
	}
	l, _ := a.List(ctx)
	if got := l[0].EnvSecrets; got["API_KEY"] != "MY_KEY" || got["BUCKET"] != "BUCKET" {
		t.Fatalf("%v", got)
	}
}

// Approvals saved before every credential became per-user are converted once: "user:NAME" becomes NAME
// and a mapping to an instance secret becomes the variable's own name, with a note for the admin.
// Approvals made afterwards, whose plain names must not be mistaken for old instance secrets, stay as they are.
func TestOlderApprovalsBecomePerUserOnce(t *testing.T) {
	a := newApprovals(t)
	old := map[string]Approval{
		"old": {Name: "old", Package: Package{RegistryType: "npm", Identifier: "x", Version: "1.0.0", Transport: "stdio"},
			EnvSecrets: map[string]string{"TOKEN": "user:GITHUB_TOKEN", "KEY": "SHARED_ADMIN_KEY"}, Enabled: true},
	}
	raw, _ := json.Marshal(old)
	if err := a.Store.SetSetting(ctx, settingKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	l, err := a.List(ctx)
	if err != nil || len(l) != 1 {
		t.Fatal(err, l)
	}
	got := l[0]
	if got.EnvSecrets["TOKEN"] != "GITHUB_TOKEN" || got.EnvSecrets["KEY"] != "KEY" {
		t.Fatalf("%v", got.EnvSecrets)
	}
	if strings.Join(got.SharedBefore, ",") != "KEY" {
		t.Fatalf("the admin must be told which variable used a shared secret: %v", got.SharedBefore)
	}
	// a later approval with a plain credential name is not converted again
	e := entries(t)[0]
	if err := a.Approve(ctx, "admin:1", e, 0, map[string]string{"BUCKET": "MY_BUCKET", "API_KEY": "MY_KEY"}); err != nil {
		t.Fatal(err)
	}
	l, _ = a.List(ctx)
	for _, ap := range l {
		if ap.Name == e.Name && (ap.EnvSecrets["API_KEY"] != "MY_KEY" || len(ap.SharedBefore) != 0) {
			t.Fatalf("a new approval was converted: %+v", ap)
		}
	}
	if err := a.ClearSharedNotice(ctx, "admin:1"); err != nil {
		t.Fatal(err)
	}
	l, _ = a.List(ctx)
	for _, ap := range l {
		if len(ap.SharedBefore) != 0 {
			t.Fatalf("the note was not cleared: %+v", ap)
		}
	}
}
