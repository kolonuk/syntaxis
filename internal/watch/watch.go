// SPDX-License-Identifier: GPL-2.0-or-later

package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Config is what a user entered for one watch.
type Config map[string]string

// Event is something new that was found.
type Event struct {
	Source  string   `json:"source"`
	Title   string   `json:"title"`
	Details []string `json:"details,omitempty"`
}

// Text is the event as plain text for a prompt.
func (e Event) Text() string {
	s := e.Source + ": " + e.Title
	for _, d := range e.Details {
		s += "\n- " + d
	}
	return s
}

// Deps are the collaborators a check needs.
type Deps struct {
	HC *http.Client
	// GitHubToken is the user's own GitHub token, if they have connected GitHub; it only raises
	// the rate limit and reaches their private repositories.
	GitHubToken string
	// BaseURL overrides the service addresses (tests).
	GitHubAPI, DockerHub, PyPI, NPM, Crates string
}

// Kind describes a kind of watch for the form.
type Kind struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Fields []Field `json:"fields"`
}

// Field is one setting of a watch.
type Field struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Hint     string `json:"hint,omitempty"`
	Required bool   `json:"required"`
}

// Kinds lists what can be watched.
func Kinds() []Kind {
	return []Kind{
		{"github_release", "GitHub release", []Field{{"repo", "Repository", "owner/name", true}}},
		{"git_ref", "Git branch or tags", []Field{{"url", "Repository address", "https://host/owner/name.git (a public repository)", true}, {"ref", "Branch, or \"tags\"", "main, or tags to notice new tags", true}}},
		{"docker_hub", "Docker Hub image", []Field{{"image", "Image", "nginx or owner/name", true}, {"tag_regex", "Only tags matching", "optional, for example ^1\\.2[0-9]\\.", false}}},
		{"package", "Package release", []Field{{"ecosystem", "npm, pypi or crates", "", true}, {"name", "Package name", "", true}}},
		{"rss", "RSS or Atom feed", []Field{{"url", "Feed address", "https://example.com/feed.xml", true}}},
		{"http_change", "Web page or file changed", []Field{{"url", "Address", "https://example.com/status", true}}},
	}
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)?$`)
var pkgRe = regexp.MustCompile(`^(@[a-z0-9_.-]+/)?[A-Za-z0-9_.-]+$`)

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("enter a full http or https address without a password")
	}
	return nil
}

// Validate checks a watch's settings.
func Validate(kind string, c Config) error {
	switch kind {
	case "github_release":
		if !repoRe.MatchString(c["repo"]) || strings.Contains(c["repo"], "..") {
			return errors.New("the repository is owner/name")
		}
	case "git_ref":
		if err := checkURL(c["url"]); err != nil {
			return err
		}
		if c["ref"] == "" || strings.ContainsAny(c["ref"], " \t\n") {
			return errors.New("enter a branch name, or tags")
		}
	case "docker_hub":
		if !imageRe.MatchString(c["image"]) {
			return errors.New("the image is nginx or owner/name")
		}
		if c["tag_regex"] != "" {
			if _, err := regexp.Compile(c["tag_regex"]); err != nil {
				return errors.New("the tag pattern is not a valid regular expression")
			}
		}
	case "package":
		if c["ecosystem"] != "npm" && c["ecosystem"] != "pypi" && c["ecosystem"] != "crates" {
			return errors.New("the ecosystem is npm, pypi or crates")
		}
		if !pkgRe.MatchString(c["name"]) {
			return errors.New("that is not a valid package name")
		}
	case "rss", "http_change":
		return checkURL(c["url"])
	default:
		return fmt.Errorf("unknown kind of watch %q", kind)
	}
	return nil
}

// Check looks at the source. prev is what the last check remembered ("" the first time). It returns
// what to remember now and, if there is something new, the event. The first check only remembers
// the current state, so creating a watch does not fire it for something that already existed.
func Check(ctx context.Context, kind string, c Config, prev string, d Deps) (state string, ev *Event, err error) {
	if err := Validate(kind, c); err != nil {
		return prev, nil, err
	}
	if d.HC == nil {
		d.HC = NewClient(false)
	}
	switch kind {
	case "github_release":
		return checkGitHubRelease(ctx, c, prev, d)
	case "git_ref":
		return checkGitRef(ctx, c, prev, d)
	case "docker_hub":
		return checkDockerHub(ctx, c, prev, d)
	case "package":
		return checkPackage(ctx, c, prev, d)
	case "rss":
		return checkRSS(ctx, c, prev, d)
	default:
		return checkHTTP(ctx, c, prev, d)
	}
}

func orDefault(s, def string) string {
	if s != "" {
		return strings.TrimRight(s, "/")
	}
	return def
}

func checkGitHubRelease(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	h := map[string]string{"Accept": "application/vnd.github+json"}
	if d.GitHubToken != "" {
		h["Authorization"] = "Bearer " + d.GitHubToken
	}
	b, code, err := get(ctx, d.HC, orDefault(d.GitHubAPI, "https://api.github.com")+"/repos/"+c["repo"]+"/releases/latest", h)
	if code == http.StatusNotFound {
		return prev, nil, errors.New("no release found (or the repository is private and GitHub is not connected)")
	}
	if err != nil {
		return prev, nil, err
	}
	var r struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.TagName == "" {
		return prev, nil, errors.New("unexpected answer from GitHub")
	}
	if prev == "" || prev == r.TagName {
		return r.TagName, nil, nil
	}
	return r.TagName, &Event{Source: "GitHub release " + c["repo"], Title: "new release " + r.TagName, Details: []string{r.Name, r.HTMLURL, clip(r.Body, 1500)}}, nil
}

func checkPackage(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	var u, field string
	switch c["ecosystem"] {
	case "npm":
		u, field = orDefault(d.NPM, "https://registry.npmjs.org")+"/"+strings.ReplaceAll(c["name"], "/", "%2f")+"/latest", "version"
	case "pypi":
		u, field = orDefault(d.PyPI, "https://pypi.org")+"/pypi/"+c["name"]+"/json", "info.version"
	default:
		u, field = orDefault(d.Crates, "https://crates.io")+"/api/v1/crates/"+c["name"], "crate.max_version"
	}
	b, _, err := get(ctx, d.HC, u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return prev, nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		return prev, nil, errors.New("unexpected answer")
	}
	var cur any = generic
	for _, p := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return prev, nil, errors.New("unexpected answer")
		}
		cur = m[p]
	}
	ver, _ := cur.(string)
	if ver == "" {
		return prev, nil, errors.New("no version in the answer")
	}
	if prev == "" || prev == ver {
		return ver, nil, nil
	}
	return ver, &Event{Source: c["ecosystem"] + " package " + c["name"], Title: "new version " + ver, Details: []string{"previous: " + prev}}, nil
}

func checkDockerHub(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	img := c["image"]
	if !strings.Contains(img, "/") {
		img = "library/" + img
	}
	b, _, err := get(ctx, d.HC, orDefault(d.DockerHub, "https://hub.docker.com")+"/v2/repositories/"+img+"/tags?page_size=50&ordering=last_updated", nil)
	if err != nil {
		return prev, nil, err
	}
	var r struct {
		Results []struct {
			Name        string `json:"name"`
			LastUpdated string `json:"last_updated"`
			Digest      string `json:"digest"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return prev, nil, errors.New("unexpected answer from Docker Hub")
	}
	var re *regexp.Regexp
	if c["tag_regex"] != "" {
		re, _ = regexp.Compile(c["tag_regex"])
	}
	for _, t := range r.Results { // newest first
		if re != nil && !re.MatchString(t.Name) {
			continue
		}
		cur := t.Name + "@" + t.Digest
		if prev == "" || prev == cur {
			return cur, nil, nil
		}
		return cur, &Event{Source: "Docker Hub " + c["image"], Title: "new or updated tag " + t.Name, Details: []string{"updated " + t.LastUpdated, "digest " + t.Digest}}, nil
	}
	return prev, nil, errors.New("no tags found" + map[bool]string{true: " matching the pattern", false: ""}[re != nil])
}

// checkGitRef reads a repository's advertised references over the plain git HTTP protocol.
func checkGitRef(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	u := strings.TrimRight(c["url"], "/")
	b, _, err := get(ctx, d.HC, u+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return prev, nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		// pkt-line: 4 hex length, then "<sha> <ref>"; skip the service header and flush packets
		if len(line) < 45 {
			continue
		}
		i := strings.Index(line, " refs/")
		if i < 40 {
			continue
		}
		sha := line[i-40 : i]
		ref := strings.TrimSpace(strings.SplitN(line[i+1:], "\x00", 2)[0])
		if !strings.HasSuffix(ref, "^{}") {
			refs[ref] = sha
		}
	}
	if len(refs) == 0 {
		return prev, nil, errors.New("no references found (is it a public repository?)")
	}
	if c["ref"] == "tags" {
		var tags []string
		for r := range refs {
			if strings.HasPrefix(r, "refs/tags/") {
				tags = append(tags, strings.TrimPrefix(r, "refs/tags/"))
			}
		}
		sort.Strings(tags)
		state := strings.Join(tags, "\n")
		if prev == "" || prev == state {
			return state, nil, nil
		}
		old := map[string]bool{}
		for _, t := range strings.Split(prev, "\n") {
			old[t] = true
		}
		var added []string
		for _, t := range tags {
			if !old[t] {
				added = append(added, t)
			}
		}
		if len(added) == 0 {
			return state, nil, nil
		}
		return state, &Event{Source: "git tags " + u, Title: fmt.Sprintf("%d new tag(s)", len(added)), Details: added}, nil
	}
	sha, ok := refs["refs/heads/"+c["ref"]]
	if !ok {
		return prev, nil, fmt.Errorf("no branch %q", c["ref"])
	}
	if prev == "" || prev == sha {
		return sha, nil, nil
	}
	return sha, &Event{Source: "git branch " + c["ref"] + " of " + u, Title: "new commits", Details: []string{"now at " + sha[:12], "was " + prev[:min(12, len(prev))]}}, nil
}

type rssFeed struct {
	Channel struct {
		Items []struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			GUID  string `xml:"guid"`
			Desc  string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
	Entries []struct {
		Title string `xml:"title"`
		ID    string `xml:"id"`
		Link  struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Summary string `xml:"summary"`
	} `xml:"entry"`
}

func checkRSS(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	b, _, err := get(ctx, d.HC, c["url"], map[string]string{"Accept": "application/rss+xml, application/atom+xml, application/xml, text/xml"})
	if err != nil {
		return prev, nil, err
	}
	var f rssFeed
	if err := xml.Unmarshal(b, &f); err != nil {
		return prev, nil, errors.New("that does not look like an RSS or Atom feed")
	}
	type entry struct{ id, title, link, desc string }
	var es []entry
	for _, it := range f.Channel.Items {
		id := firstNonEmpty(it.GUID, it.Link, it.Title)
		es = append(es, entry{id, it.Title, it.Link, it.Desc})
	}
	for _, e := range f.Entries {
		id := firstNonEmpty(e.ID, e.Link.Href, e.Title)
		es = append(es, entry{id, e.Title, e.Link.Href, e.Summary})
	}
	if len(es) == 0 {
		return prev, nil, errors.New("the feed has no entries")
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range es {
		ids = append(ids, e.id)
		seen[e.id] = true
	}
	if len(ids) > 100 {
		ids = ids[:100]
	}
	state, _ := json.Marshal(ids)
	if prev == "" {
		return string(state), nil, nil
	}
	var old []string
	_ = json.Unmarshal([]byte(prev), &old)
	was := map[string]bool{}
	for _, o := range old {
		was[o] = true
	}
	var fresh []string
	for _, e := range es {
		if !was[e.id] {
			fresh = append(fresh, strings.TrimSpace(e.title+" "+e.link)+clipPrefix(e.desc))
			if len(fresh) == 10 {
				break
			}
		}
	}
	if len(fresh) == 0 {
		return string(state), nil, nil
	}
	return string(state), &Event{Source: "feed " + c["url"], Title: fmt.Sprintf("%d new entr(ies)", len(fresh)), Details: fresh}, nil
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func clipPrefix(s string) string {
	s = strings.TrimSpace(tagRe.ReplaceAllString(s, " "))
	if s == "" {
		return ""
	}
	return " (" + clip(strings.Join(strings.Fields(s), " "), 200) + ")"
}

func checkHTTP(ctx context.Context, c Config, prev string, d Deps) (string, *Event, error) {
	b, _, err := get(ctx, d.HC, c["url"], nil)
	if err != nil {
		return prev, nil, err
	}
	h := sha256.Sum256(b)
	cur := hex.EncodeToString(h[:])
	if prev == "" || prev == cur {
		return cur, nil, nil
	}
	return cur, &Event{Source: c["url"], Title: "the page changed", Details: []string{clip(strings.Join(strings.Fields(tagRe.ReplaceAllString(string(b), " ")), " "), 1500)}}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
