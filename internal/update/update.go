// SPDX-License-Identifier: GPL-2.0-or-later

// Package update finds out which releases of Syntaxis exist and what the development branch is at,
// so the admin app can offer an update. It only reads; the update itself is done on the Docker host
// by scripts/update.sh, started by the host's update service.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is where releases come from.
const DefaultRepo = "kolonuk/syntaxis"

// Release is a published version.
type Release struct {
	Tag        string    `json:"tag"`
	Name       string    `json:"name"`
	Published  time.Time `json:"published"`
	URL        string    `json:"url"`
	Prerelease bool      `json:"prerelease"`
}

// Edge is the newest commit on the main branch.
type Edge struct {
	SHA     string    `json:"sha"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
}

// Source reads releases and the main branch from GitHub, and remembers the answer for a while so a
// busy admin page does not use up the unauthenticated rate limit.
type Source struct {
	Repo string
	Base string // for tests of the parser against another repository's address; default the real API
	HC   *http.Client
	TTL  time.Duration
	Now  func() time.Time

	mu       sync.Mutex
	fetched  time.Time
	releases []Release
	edge     Edge
	err      error
}

func (s *Source) base() string {
	if s.Base != "" {
		return strings.TrimRight(s.Base, "/")
	}
	return "https://api.github.com"
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) get(ctx context.Context, path string, out any) error {
	hc := s.HC
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("could not reach GitHub")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity: // 422: no such branch
		return errNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("GitHub is limiting requests from this address; try again later")
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

var errNotFound = errors.New("not found")

// Refresh reads the releases and the main branch now.
func (s *Source) Refresh(ctx context.Context) ([]Release, Edge, error) {
	repo := s.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	var rel []struct {
		Tag        string    `json:"tag_name"`
		Name       string    `json:"name"`
		Published  time.Time `json:"published_at"`
		URL        string    `json:"html_url"`
		Prerelease bool      `json:"prerelease"`
		Draft      bool      `json:"draft"`
	}
	if err := s.get(ctx, "/repos/"+repo+"/releases?per_page=20", &rel); err != nil && !errors.Is(err, errNotFound) {
		return nil, Edge{}, err
	}
	var releases []Release
	for _, r := range rel {
		if !r.Draft && ValidTag(r.Tag) {
			releases = append(releases, Release{Tag: r.Tag, Name: r.Name, Published: r.Published, URL: r.URL, Prerelease: r.Prerelease})
		}
	}
	var c struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	var edge Edge
	if err := s.get(ctx, "/repos/"+repo+"/commits/main", &c); err == nil {
		edge = Edge{SHA: c.SHA, Date: c.Commit.Author.Date, Subject: strings.SplitN(c.Commit.Message, "\n", 2)[0]}
	} else if !errors.Is(err, errNotFound) {
		return releases, Edge{}, err
	}
	return releases, edge, nil
}

// Check is Refresh with a memory: the last answer is reused for TTL (default an hour) unless force.
func (s *Source) Check(ctx context.Context, force bool) ([]Release, Edge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ttl := s.TTL
	if ttl == 0 {
		ttl = time.Hour
	}
	if !force && !s.fetched.IsZero() && s.now().Sub(s.fetched) < ttl {
		return s.releases, s.edge, s.err
	}
	r, e, err := s.Refresh(ctx)
	s.fetched, s.releases, s.edge, s.err = s.now(), r, e, err
	return r, e, err
}

var tagRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// ValidTag reports whether s looks like a version tag. Anything else is never passed to the host.
func ValidTag(s string) bool { return len(s) <= 40 && tagRe.MatchString(s) }

// The release streams. A stream is what an install follows: stable is the newest published release
// that is not marked a pre-release, preview is the newest published release of any kind (release
// candidates and betas included), and edge is the main branch built from source on the host.
const (
	StreamStable  = "stable"
	StreamPreview = "preview"
	StreamEdge    = "edge"
)

// Streams lists them in the order the admin app shows them, the safest first.
var Streams = []string{StreamStable, StreamPreview, StreamEdge}

// ValidStream reports whether s names a stream.
func ValidStream(s string) bool { return s == StreamStable || s == StreamPreview || s == StreamEdge }

// ValidTarget accepts what an admin may ask the host to update to: a stream (or "latest", the old
// name for stable) or one version tag.
func ValidTarget(s string) bool { return s == "latest" || ValidStream(s) || ValidTag(s) }

// LatestIn returns the newest release a stream would install: stable skips pre-releases, preview
// takes the newest of all. Edge has no releases.
func LatestIn(rs []Release, stream string) (Release, bool) {
	var best Release
	found := false
	for _, r := range rs {
		if stream == StreamStable && r.Prerelease {
			continue
		}
		if stream == StreamEdge {
			return Release{}, false
		}
		if !found || Newer(r.Tag, best.Tag) {
			best, found = r, true
		}
	}
	return best, found
}

// StreamOf says which stream an install is on. The host records it as SYNTAXIS_CHANNEL when it
// updates ("release", the old name, means stable). An install that never recorded one is judged by
// what it runs: a pre-release tag is preview, any other tag is stable, anything else is edge.
func StreamOf(version, channel string) string {
	switch channel {
	case StreamStable, "release":
		return StreamStable
	case StreamPreview:
		return StreamPreview
	case StreamEdge:
		return StreamEdge
	}
	if ValidTag(version) {
		if strings.Contains(version, "-") {
			return StreamPreview
		}
		return StreamStable
	}
	return StreamEdge
}

// Latest returns the newest stable release (a prerelease only if there is nothing else).
func Latest(rs []Release) (Release, bool) {
	var best Release
	found := false
	for _, r := range rs {
		if !found || (best.Prerelease && !r.Prerelease) || (r.Prerelease == best.Prerelease && Newer(r.Tag, best.Tag)) {
			best, found = r, true
		}
	}
	return best, found
}

// Newer reports whether version a is higher than b. Versions are vMAJOR.MINOR.PATCH with an optional
// prerelease suffix, which ranks below the same version without one.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka {
		return false
	}
	if !okb {
		return true
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	switch {
	case pa.pre == pb.pre:
		return false
	case pa.pre == "":
		return true
	case pb.pre == "":
		return false
	}
	return pa.pre > pb.pre
}

type ver struct {
	n   [3]int
	pre string
}

func parse(s string) (ver, bool) {
	if !tagRe.MatchString(s) {
		return ver{}, false
	}
	s = strings.TrimPrefix(s, "v")
	var v ver
	if i := strings.Index(s, "-"); i >= 0 {
		v.pre, s = s[i+1:], s[:i]
	}
	for i, p := range strings.Split(s, ".") {
		v.n[i], _ = strconv.Atoi(p)
	}
	return v, true
}
