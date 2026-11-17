// SPDX-License-Identifier: GPL-2.0-or-later

package update

import (
	"context"
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
	for _, ok := range []string{"latest", "edge", "v1.2.3", "1.2.3", "v1.2.3-rc.1"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "main", "v1.2", "v1.2.3; rm -rf /", "$(id)", "v1.2.3 --edge", "../v1.0.0", "v1.2.3\nedge", "-v1.2.3"} {
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
	if _, _, err := s.Check(context.Background(), false); err != nil {
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
