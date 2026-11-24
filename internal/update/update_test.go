// SPDX-License-Identifier: GPL-2.0-or-later

package update

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

func TestVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true}, {"v1.10.0", "v1.9.0", true}, {"1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-rc1", true}, {"v1.0.0-rc2", "v1.0.0-rc1", true}, {"v1.0.0-rc1", "v1.0.0", false},
		{"v2.0.0", "dev", true}, {"dev", "v1.0.0", false}, {"nonsense", "v1.0.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestOnlyTagsAndKeywordsAreTargets(t *testing.T) {
	for _, ok := range []string{"latest", "stable", "preview", "edge", "v1.2.3", "1.2.3", "v1.2.3-rc.1"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "main", "beta", "Stable", "v1.2", "v1.2.3; rm -rf /", "$(id)", "v1.2.3 --edge", "../v1.0.0", "v1.2.3\nedge", "-v1.2.3"} {
		if ValidTarget(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLatestPrefersStable(t *testing.T) {
	r, ok := Latest([]Release{{Tag: "v2.0.0-rc1", Prerelease: true}, {Tag: "v1.9.0"}, {Tag: "v1.10.0"}})
	if !ok || r.Tag != "v1.10.0" {
		t.Fatalf("%+v", r)
	}
	if _, ok := Latest(nil); ok {
		t.Fatal("no releases")
	}
}

// The real GitHub API: a repository with many releases for the parser, and this project's own main
// branch (which has no releases yet, so an empty list is a valid answer).
func TestRealGitHub(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	ctx := context.Background()
	s := &Source{Repo: "docker/compose"}
	rs, edge, err := s.Refresh(ctx)
	skipIfGitHubUnavailable(t, err)
	if err != nil || len(rs) == 0 || edge.SHA == "" {
		t.Fatalf("%v %d %+v", err, len(rs), edge)
	}
	latest, _ := Latest(rs)
	if !ValidTag(latest.Tag) || latest.Published.IsZero() || latest.URL == "" {
		t.Fatalf("%+v", latest)
	}
	own := &Source{}
	if _, e, err := own.Refresh(ctx); err != nil || e.SHA == "" || e.Subject == "" {
		t.Fatalf("this project's main branch: %v %+v", err, e)
	}
	if _, _, err := (&Source{Repo: "cli/this-repository-does-not-exist-xyz"}).Refresh(ctx); err != nil {
		t.Fatalf("a repository without releases or main is not an error: %v", err)
	}
}

func TestCheckRemembersTheAnswer(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	now := time.Now()
	s := &Source{Repo: "docker/compose", TTL: time.Hour, Now: func() time.Time { return now }}
	_, _, err := s.Check(context.Background(), false)
	skipIfGitHubUnavailable(t, err)
	if err != nil {
		t.Fatal(err)
	}
	s.Base = "http://127.0.0.1:1" // would fail if asked again
	if _, e, err := s.Check(context.Background(), false); err != nil || e.SHA == "" {
		t.Fatalf("the remembered answer was not used: %v", err)
	}
	if _, _, err := s.Check(context.Background(), true); err == nil {
		t.Fatal("forcing a refresh must ask again")
	}
}

// The real GitHub API is sometimes down (a 5xx) or limits an address. That is a test that could not
// run, not a failure of this code.
func skipIfGitHubUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if msg := err.Error(); strings.Contains(msg, "HTTP 5") || strings.Contains(msg, "limiting") || strings.Contains(msg, "rate limit") {
		testenv.NotTested(t, "the real GitHub API is not answering normally: "+msg)
	}
}

func TestStreamsPickTheRightRelease(t *testing.T) {
	rs := []Release{
		{Tag: "v0.3.0-rc.2", Prerelease: true}, {Tag: "v0.3.0-rc.1", Prerelease: true},
		{Tag: "v0.2.1"}, {Tag: "v0.2.0"}, {Tag: "v0.1.9"},
	}
	if r, ok := LatestIn(rs, StreamStable); !ok || r.Tag != "v0.2.1" {
		t.Fatalf("stable must skip pre-releases: %+v", r)
	}
	if r, ok := LatestIn(rs, StreamPreview); !ok || r.Tag != "v0.3.0-rc.2" {
		t.Fatalf("preview takes the newest of all: %+v", r)
	}
	// once the final release is out, preview moves to it rather than staying on the candidate
	rs = append([]Release{{Tag: "v0.3.0"}}, rs...)
	if r, _ := LatestIn(rs, StreamPreview); r.Tag != "v0.3.0" {
		t.Fatalf("a final release outranks its own candidates: %+v", r)
	}
	if _, ok := LatestIn(rs, StreamEdge); ok {
		t.Fatal("edge has no releases")
	}
	if _, ok := LatestIn([]Release{{Tag: "v1.0.0-rc.1", Prerelease: true}}, StreamStable); ok {
		t.Fatal("stable must not fall back to a pre-release")
	}
	if _, ok := LatestIn(nil, StreamPreview); ok {
		t.Fatal("no releases")
	}
}

func TestStreamOfAnInstall(t *testing.T) {
	for _, tc := range []struct{ version, channel, want string }{
		{"v0.2.1", "", StreamStable}, {"v0.3.0-rc.1", "", StreamPreview}, {"dev", "", StreamEdge}, {"abc1234", "", StreamEdge},
		{"v0.2.1", "release", StreamStable}, {"v0.2.1", "stable", StreamStable}, {"v0.3.0-rc.1", "preview", StreamPreview},
		{"v0.2.1", "edge", StreamEdge}, {"v0.2.1", "nonsense", StreamStable},
	} {
		if got := StreamOf(tc.version, tc.channel); got != tc.want {
			t.Errorf("StreamOf(%q, %q) = %q, want %q", tc.version, tc.channel, got, tc.want)
		}
	}
	for _, s := range Streams {
		if !ValidStream(s) {
			t.Errorf("%s is not valid", s)
		}
	}
	if ValidStream("latest") || ValidStream("") {
		t.Fatal("only the three streams are streams")
	}
}

// A failed look-up is retried within a minute, not remembered for the hour a good answer is.
func TestCheckRetriesAFailureSoon(t *testing.T) {
	now := time.Now()
	s := &Source{Base: "http://127.0.0.1:1", TTL: time.Hour, Now: func() time.Time { return now }} // nothing listens
	if _, _, err := s.Check(context.Background(), false); err == nil {
		t.Fatal("a look-up against nothing succeeded")
	}
	// the failure is remembered briefly: a second request straight away does not ask again
	s.Base = "http://127.0.0.1:2"
	if _, _, err := s.Check(context.Background(), false); err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("%v", err)
	}
	// after a minute it does ask again (and would now reach the address that answers)
	now = now.Add(61 * time.Second)
	s.mu.Lock()
	remembered := !s.fetched.IsZero() && now.Sub(s.fetched) < s.TTL
	s.mu.Unlock()
	if remembered {
		t.Fatal("a failure was still remembered after a minute")
	}
}
