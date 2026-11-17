// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitHub connects a user's GitHub account with a fine-grained personal access token.
type GitHub struct {
	HC      *http.Client
	APIBase string // default https://api.github.com (tests point it elsewhere)
}

func (g *GitHub) base() string {
	if g.APIBase != "" {
		return g.APIBase
	}
	return "https://api.github.com"
}

func (g *GitHub) hdr(c Creds) map[string]string {
	h := bearer(c["GITHUB_TOKEN"])
	h["Accept"] = "application/vnd.github+json"
	h["X-GitHub-Api-Version"] = "2022-11-28"
	return h
}

// Kind implements Provider.
func (g *GitHub) Kind() Kind {
	return Kind{ID: "github", Title: "GitHub", Git: true,
		Guide:  "On GitHub open Settings, Developer settings, Fine-grained tokens, and create a token for only the repositories you want to use here. Give it Contents (read and write) and Pull requests (read and write); add Administration (read and write) only if you want to create new repositories from Syntaxis. Set an expiry.",
		Fields: []Field{{Name: "GITHUB_TOKEN", Label: "Fine-grained access token", Secret: true, Required: true}}}
}

// Test implements Provider.
func (g *GitHub) Test(ctx context.Context, c Creds) (Identity, error) {
	var u struct{ Login, Name string }
	if _, err := call(ctx, g.HC, "GET", g.base()+"/user", g.hdr(c), nil, &u); err != nil {
		return Identity{}, err
	}
	return Identity{Account: u.Login, Detail: u.Name}, nil
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

func (r ghRepo) repo() Repo {
	return Repo{FullName: r.FullName, Description: r.Description, Private: r.Private, CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, WebURL: r.HTMLURL}
}

// ListRepos implements GitProvider: the repositories the token can reach, most recently pushed first.
func (g *GitHub) ListRepos(ctx context.Context, c Creds, query string) ([]Repo, error) {
	var rs []ghRepo
	if _, err := call(ctx, g.HC, "GET", g.base()+"/user/repos?per_page=100&sort=pushed", g.hdr(c), nil, &rs); err != nil {
		return nil, err
	}
	return filterRepos(rs, query, func(r ghRepo) Repo { return r.repo() }), nil
}

// CreateRepo implements GitProvider.
func (g *GitHub) CreateRepo(ctx context.Context, c Creds, n NewRepo) (Repo, error) {
	var r ghRepo
	body := jsonBody(map[string]any{"name": n.Name, "description": n.Description, "private": n.Private, "auto_init": true})
	if _, err := call(ctx, g.HC, "POST", g.base()+"/user/repos", g.hdr(c), body, &r); err != nil {
		return Repo{}, err
	}
	return r.repo(), nil
}

// OpenPR implements GitProvider.
func (g *GitHub) OpenPR(ctx context.Context, c Creds, pr PullRequest) (string, error) {
	if !validFullName(pr.Repo) {
		return "", fmt.Errorf("bad repository name %q", pr.Repo)
	}
	base := pr.Base
	if base == "" {
		var r ghRepo
		if _, err := call(ctx, g.HC, "GET", g.base()+"/repos/"+pr.Repo, g.hdr(c), nil, &r); err != nil {
			return "", err
		}
		base = r.DefaultBranch
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	body := jsonBody(map[string]any{"title": pr.Title, "body": pr.Body, "head": pr.Head, "base": base})
	if _, err := call(ctx, g.HC, "POST", g.base()+"/repos/"+pr.Repo+"/pulls", g.hdr(c), body, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

// ReadFile implements GitProvider.
func (g *GitHub) ReadFile(ctx context.Context, c Creds, repo, ref, path string) ([]byte, error) {
	if !validFullName(repo) || !safeRepoPath(path) || !safeRef(ref) {
		return nil, fmt.Errorf("bad repository, path or branch")
	}
	h := g.hdr(c)
	h["Accept"] = "application/vnd.github.raw+json"
	u := g.base() + "/repos/" + repo + "/contents/" + path
	if ref != "" {
		u += "?ref=" + ref
	}
	return callRaw(ctx, g.HC, u, h)
}

func filterRepos[T any](in []T, query string, conv func(T) Repo) []Repo {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Repo
	for _, x := range in {
		r := conv(x)
		if q == "" || strings.Contains(strings.ToLower(r.FullName+" "+r.Description), q) {
			out = append(out, r)
		}
	}
	return out
}

// validFullName accepts owner/name pairs (GitLab allows nested groups) made of safe characters, so
// a repository name can be put in a URL path.
func validFullName(s string) bool {
	if s == "" || strings.Count(s, "/") < 1 || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
			return false
		}
	}
	return true
}

func escapePath(s string) string { return url.PathEscape(s) }
