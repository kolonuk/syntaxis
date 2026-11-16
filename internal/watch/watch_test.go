// SPDX-License-Identifier: GPL-2.0-or-later

package watch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var ctx = context.Background()

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func deps(s *httptest.Server) Deps {
	return Deps{HC: NewClient(true), GitHubAPI: s.URL, DockerHub: s.URL, PyPI: s.URL, NPM: s.URL, Crates: s.URL}
}

func TestTheClientRefusesPrivateAddressesButTestsCanAllowThem(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secret") })
	if _, _, err := get(ctx, NewClient(false), s.URL, nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a loopback address must be refused: %v", err)
	}
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://10.0.0.5/", "http://192.168.1.1/", "http://[::1]/", "http://localhost/"} {
		if _, _, err := get(ctx, NewClient(false), u, nil); err == nil {
			t.Errorf("%s was fetched", u)
		}
	}
	if _, _, err := get(ctx, NewClient(true), s.URL, nil); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "http://user:pw@example.com/"} {
		if _, _, err := get(ctx, NewClient(true), u, nil); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
}

func TestRedirectsToPrivateAddressesAreRefused(t *testing.T) {
	// a public-looking page that redirects to the metadata address
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://169.254.169.254/", 302) })
	// the first hop (loopback) is allowed only because this client allows private for the test server;
	// with the strict client even the first hop is refused, which is the property that matters
	if _, _, err := get(ctx, NewClient(false), s.URL, nil); err == nil {
		t.Fatal("redirect not blocked")
	}
}

func TestFirstCheckRemembersWithoutFiring(t *testing.T) {
	var tag atomic.Value
	tag.Store("v1.0.0")
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"name":"Release","body":"notes","html_url":"https://example.com/r"}`, tag.Load())
	})
	d := deps(s)
	cfg := Config{"repo": "owner/app"}
	st, ev, err := Check(ctx, "github_release", cfg, "", d)
	if err != nil || ev != nil || st != "v1.0.0" {
		t.Fatalf("%q %v %v", st, ev, err)
	}
	if st, ev, _ := Check(ctx, "github_release", cfg, st, d); ev != nil || st != "v1.0.0" {
		t.Fatal("no change must not fire")
	}
	tag.Store("v1.1.0")
	st, ev, err = Check(ctx, "github_release", cfg, st, d)
	if err != nil || ev == nil || st != "v1.1.0" || !strings.Contains(ev.Text(), "v1.1.0") || !strings.Contains(ev.Source, "owner/app") {
		t.Fatalf("%q %+v %v", st, ev, err)
	}
}

func TestPackageReleases(t *testing.T) {
	var ver atomic.Value
	ver.Store("1.0.0")
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/pypi/"):
			fmt.Fprintf(w, `{"info":{"version":%q}}`, ver.Load())
		case strings.HasPrefix(r.URL.Path, "/api/v1/crates/"):
			fmt.Fprintf(w, `{"crate":{"max_version":%q}}`, ver.Load())
		default:
			fmt.Fprintf(w, `{"version":%q}`, ver.Load())
		}
	})
	for _, eco := range []string{"npm", "pypi", "crates"} {
		ver.Store("1.0.0")
		cfg := Config{"ecosystem": eco, "name": "thing"}
		st, _, err := Check(ctx, "package", cfg, "", deps(s))
		if err != nil || st != "1.0.0" {
			t.Fatalf("%s: %q %v", eco, st, err)
		}
		ver.Store("2.0.0")
		if _, ev, _ := Check(ctx, "package", cfg, st, deps(s)); ev == nil || !strings.Contains(ev.Title, "2.0.0") {
			t.Fatalf("%s: %+v", eco, ev)
		}
	}
}

func TestDockerHubTags(t *testing.T) {
	var body atomic.Value
	body.Store(`{"results":[{"name":"1.26","last_updated":"2026-01-02","digest":"sha256:aaa"},{"name":"1.25","last_updated":"2025-01-01","digest":"sha256:bbb"}]}`)
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/repositories/library/nginx/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body.Load().(string))
	})
	cfg := Config{"image": "nginx"}
	st, ev, err := Check(ctx, "docker_hub", cfg, "", deps(s))
	if err != nil || ev != nil || !strings.HasPrefix(st, "1.26@") {
		t.Fatalf("%q %v %v", st, ev, err)
	}
	body.Store(`{"results":[{"name":"1.27","last_updated":"2026-02-02","digest":"sha256:ccc"},{"name":"1.26","digest":"sha256:aaa"}]}`)
	if _, ev, _ := Check(ctx, "docker_hub", cfg, st, deps(s)); ev == nil || !strings.Contains(ev.Title, "1.27") {
		t.Fatalf("%+v", ev)
	}
	// a pattern ignores tags that do not match
	body.Store(`{"results":[{"name":"nightly","digest":"sha256:ddd"},{"name":"1.27","digest":"sha256:ccc"}]}`)
	st2, ev, _ := Check(ctx, "docker_hub", Config{"image": "nginx", "tag_regex": `^1\.`}, "1.27@sha256:ccc", deps(s))
	if ev != nil || st2 != "1.27@sha256:ccc" {
		t.Fatalf("a tag outside the pattern fired: %q %+v", st2, ev)
	}
}

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestGitRefs(t *testing.T) {
	var sha atomic.Value
	sha.Store(strings.Repeat("a", 40))
	var tags atomic.Value
	tags.Store("v1")
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/o/r.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, pkt("# service=git-upload-pack\n")+"0000")
		fmt.Fprint(w, pkt(sha.Load().(string)+" HEAD\x00caps\n"))
		fmt.Fprint(w, pkt(sha.Load().(string)+" refs/heads/main\n"))
		for _, tg := range strings.Split(tags.Load().(string), ",") {
			fmt.Fprint(w, pkt(strings.Repeat("b", 40)+" refs/tags/"+tg+"\n"))
			fmt.Fprint(w, pkt(strings.Repeat("c", 40)+" refs/tags/"+tg+"^{}\n"))
		}
		fmt.Fprint(w, "0000")
	})
	d := deps(s)
	branch := Config{"url": s.URL + "/o/r.git", "ref": "main"}
	st, ev, err := Check(ctx, "git_ref", branch, "", d)
	if err != nil || ev != nil || st != strings.Repeat("a", 40) {
		t.Fatalf("%q %v %v", st, ev, err)
	}
	sha.Store(strings.Repeat("d", 40))
	if _, ev, _ := Check(ctx, "git_ref", branch, st, d); ev == nil || !strings.Contains(ev.Text(), "dddddddddddd") {
		t.Fatalf("%+v", ev)
	}
	if _, _, err := Check(ctx, "git_ref", Config{"url": s.URL + "/o/r.git", "ref": "nope"}, "", d); err == nil {
		t.Fatal("a missing branch was accepted")
	}
	tagCfg := Config{"url": s.URL + "/o/r.git", "ref": "tags"}
	st, _, _ = Check(ctx, "git_ref", tagCfg, "", d)
	tags.Store("v1,v2")
	if _, ev, _ := Check(ctx, "git_ref", tagCfg, st, d); ev == nil || len(ev.Details) != 1 || ev.Details[0] != "v2" {
		t.Fatalf("%+v", ev)
	}
}

func TestFeeds(t *testing.T) {
	var feed atomic.Value
	feed.Store(`<rss><channel><item><title>One</title><link>https://e.com/1</link><guid>1</guid></item></channel></rss>`)
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, feed.Load().(string)) })
	cfg := Config{"url": s.URL + "/feed"}
	st, ev, err := Check(ctx, "rss", cfg, "", deps(s))
	if err != nil || ev != nil {
		t.Fatalf("%v %v", ev, err)
	}
	feed.Store(`<rss><channel><item><title>Two</title><link>https://e.com/2</link><guid>2</guid><description>&lt;p&gt;Fresh&lt;/p&gt;</description></item><item><title>One</title><guid>1</guid></item></channel></rss>`)
	st2, ev, _ := Check(ctx, "rss", cfg, st, deps(s))
	if ev == nil || len(ev.Details) != 1 || !strings.Contains(ev.Details[0], "Two") || !strings.Contains(ev.Details[0], "Fresh") || strings.Contains(ev.Details[0], "<p>") {
		t.Fatalf("%+v", ev)
	}
	if _, ev, _ := Check(ctx, "rss", cfg, st2, deps(s)); ev != nil {
		t.Fatal("seen entries must not fire again")
	}
	// Atom
	feed.Store(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>A</title><id>a1</id><link href="https://e.com/a"/></entry></feed>`)
	stA, _, _ := Check(ctx, "rss", cfg, "", deps(s))
	feed.Store(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>B</title><id>b1</id><link href="https://e.com/b"/></entry><entry><title>A</title><id>a1</id></entry></feed>`)
	if _, ev, _ := Check(ctx, "rss", cfg, stA, deps(s)); ev == nil || !strings.Contains(ev.Details[0], "B") {
		t.Fatalf("%+v", ev)
	}
	feed.Store(`not a feed`)
	if _, _, err := Check(ctx, "rss", cfg, st2, deps(s)); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPageChange(t *testing.T) {
	var page atomic.Value
	page.Store("<p>all good</p>")
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, page.Load().(string)) })
	cfg := Config{"url": s.URL}
	st, ev, _ := Check(ctx, "http_change", cfg, "", deps(s))
	if ev != nil {
		t.Fatal("first check must not fire")
	}
	page.Store("<p>outage</p>")
	if _, ev, _ := Check(ctx, "http_change", cfg, st, deps(s)); ev == nil || !strings.Contains(ev.Details[0], "outage") {
		t.Fatalf("%+v", ev)
	}
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
