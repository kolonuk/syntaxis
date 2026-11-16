// SPDX-License-Identifier: GPL-2.0-or-later

package watch

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

var ctx = context.Background()

// The checks run against the real services. A change is simulated by starting from an older
// remembered state, which is exactly what a stored state is, so the service's own answer is what
// gets parsed.

func TestTheClientRefusesPrivateAddresses(t *testing.T) {
	// A real listener on loopback: the client must not connect to it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	if _, _, err := get(ctx, NewClient(), "http://"+l.Addr().String()+"/", nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a loopback address must be refused: %v", err)
	}
	select {
	case <-accepted:
		t.Fatal("the client connected to a loopback address")
	default:
	}
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://10.0.0.5/", "http://192.168.1.1/", "http://[::1]/", "http://localhost/"} {
		if _, _, err := get(ctx, NewClient(), u, nil); err == nil {
			t.Errorf("%s was fetched", u)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "http://user:pw@example.com/"} {
		if _, _, err := get(ctx, NewClient(), u, nil); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
}

func TestRedirectsToPrivateAddressesAreRefused(t *testing.T) {
	testenv.Network(t, "httpbin.org:443")
	// a real public service that redirects to the cloud metadata address
	_, _, err := get(ctx, NewClient(), "https://httpbin.org/redirect-to?url=http%3A%2F%2F169.254.169.254%2F&status_code=302", nil)
	if err == nil {
		t.Fatal("the redirect to a private address was followed")
	}
}

// fires checks a source: the first look remembers without firing, looking again changes nothing,
// and starting from an older remembered state fires with what the service really says.
func fires(t *testing.T, kind string, cfg Config, old string, want func(*Event) bool) {
	t.Helper()
	st, ev, err := Check(ctx, kind, cfg, "", Deps{})
	if err != nil || ev != nil || st == "" {
		t.Fatalf("first check: %q %v %v", st, ev, err)
	}
	if st2, ev, err := Check(ctx, kind, cfg, st, Deps{}); err != nil || ev != nil || st2 != st {
		t.Fatalf("no change must not fire: %q %+v %v", st2, ev, err)
	}
	st3, ev, err := Check(ctx, kind, cfg, old, Deps{})
	if err != nil || ev == nil || st3 != st {
		t.Fatalf("from an older state: %q %+v %v", st3, ev, err)
	}
	if want != nil && !want(ev) {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestGitHubReleases(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	fires(t, "github_release", Config{"repo": "cli/cli"}, "v0.0.1", func(e *Event) bool {
		return strings.Contains(e.Source, "cli/cli") && strings.Contains(e.Text(), "v")
	})
	if _, _, err := Check(ctx, "github_release", Config{"repo": "cli/this-repository-does-not-exist-xyz"}, "", Deps{}); err == nil {
		t.Fatal("a missing repository was accepted")
	}
}

func TestPackageReleases(t *testing.T) {
	testenv.Network(t, "registry.npmjs.org:443")
	for eco, name := range map[string]string{"npm": "express", "pypi": "requests", "crates": "serde"} {
		fires(t, "package", Config{"ecosystem": eco, "name": name}, "0.0.0", func(e *Event) bool { return e.Title != "" })
	}
}

func TestDockerHubTags(t *testing.T) {
	testenv.Network(t, "hub.docker.com:443")
	fires(t, "docker_hub", Config{"image": "nginx", "tag_regex": `^[0-9]+\.[0-9]+`}, "0.0.1@sha256:old", func(e *Event) bool { return e.Title != "" })
}

func TestGitRefs(t *testing.T) {
	testenv.Network(t, "github.com:443")
	fires(t, "git_ref", Config{"url": "https://github.com/octocat/Hello-World.git", "ref": "master"}, strings.Repeat("0", 40), func(e *Event) bool { return e.Text() != "" })
	if _, _, err := Check(ctx, "git_ref", Config{"url": "https://github.com/octocat/Hello-World.git", "ref": "no-such-branch-xyz"}, "", Deps{}); err == nil {
		t.Fatal("a missing branch was accepted")
	}
}

func TestFeeds(t *testing.T) {
	testenv.Network(t, "go.dev:443")
	cfg := Config{"url": "https://go.dev/blog/feed.atom"}
	st, ev, err := Check(ctx, "rss", cfg, "", Deps{})
	if err != nil || ev != nil {
		t.Fatalf("%v %v", ev, err)
	}
	// forgetting the newest entry makes it new again
	_, ev, err = Check(ctx, "rss", cfg, `["something-else"]`, Deps{})
	if err != nil || ev == nil || len(ev.Details) == 0 || strings.Contains(ev.Details[0], "<p>") {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, ev, _ := Check(ctx, "rss", cfg, st, Deps{}); ev != nil {
		t.Fatal("seen entries must not fire again")
	}
	// a page that is not a feed
	if _, _, err := Check(ctx, "rss", Config{"url": "https://example.com/"}, "", Deps{}); err == nil {
		t.Fatal("a web page was accepted as a feed")
	}
}

func TestPageChange(t *testing.T) {
	testenv.Network(t, "example.com:443")
	fires(t, "http_change", Config{"url": "https://example.com/"}, "an older fingerprint", nil)
}

func TestValidate(t *testing.T) {
	good := map[string]Config{
		"github_release": {"repo": "a/b"}, "git_ref": {"url": "https://x.example.com/a/b.git", "ref": "main"},
		"docker_hub": {"image": "nginx", "tag_regex": "^1"}, "package": {"ecosystem": "npm", "name": "@scope/pkg"},
		"rss": {"url": "https://x.example.com/f"}, "http_change": {"url": "http://x.example.com/"},
	}
	for k, c := range good {
		if err := Validate(k, c); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	bad := map[string]Config{
		"github_release": {"repo": "../etc"}, "git_ref": {"url": "ftp://x", "ref": "main"}, "docker_hub": {"image": "A B"},
		"package": {"ecosystem": "gem", "name": "x"}, "rss": {"url": "javascript:alert(1)"}, "http_change": {"url": "https://u:p@x.example.com"},
	}
	for k, c := range bad {
		if Validate(k, c) == nil {
			t.Errorf("%s accepted %v", k, c)
		}
	}
	if Validate("telepathy", Config{}) == nil {
		t.Error("unknown kind accepted")
	}
	if len(Kinds()) != 6 {
		t.Error("kinds")
	}
}
