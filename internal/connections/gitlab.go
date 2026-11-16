// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitLab connects a user's GitLab account (gitlab.com or their own server).
type GitLab struct{ HC *http.Client }

func glBase(c Creds) string {
	h := strings.TrimRight(strings.TrimSpace(c["GITLAB_HOST"]), "/")
	if h == "" {
		h = "https://gitlab.com"
	}
	return h
}

func glHdr(c Creds) map[string]string {
	return map[string]string{"PRIVATE-TOKEN": c["GITLAB_TOKEN"], "Accept": "application/json", "Content-Type": "application/json"}
}

// Kind implements Provider.
func (g *GitLab) Kind() Kind {
	return Kind{ID: "gitlab", Title: "GitLab", Git: true,
		Guide: "In GitLab open Preferences, Access tokens, and create a personal access token with the api scope (or read_repository and write_repository for git only), with an expiry. For a self-hosted GitLab, enter its address below.",
		Fields: []Field{
			{Name: "GITLAB_TOKEN", Label: "Personal access token", Secret: true, Required: true},
			{Name: "GITLAB_HOST", Label: "GitLab address", Hint: "Leave empty for gitlab.com, or https://gitlab.example.com", Default: "https://gitlab.com"},
		}}
}

func checkHost(c Creds) error {
	if h := glBase(c); !strings.HasPrefix(h, "https://") && !strings.HasPrefix(h, "http://") {
		return fmt.Errorf("the address must start with https://")
	}
	return nil
}

// Test implements Provider.
func (g *GitLab) Test(ctx context.Context, c Creds) (Identity, error) {
	if err := checkHost(c); err != nil {
		return Identity{}, err
	}
	var u struct{ Username, Name string }
	if _, err := call(ctx, g.HC, "GET", glBase(c)+"/api/v4/user", glHdr(c), nil, &u); err != nil {
		return Identity{}, err
	}
	return Identity{Account: u.Username, Detail: u.Name}, nil
}

type glProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	Description       string `json:"description"`
	Visibility        string `json:"visibility"`
	HTTPURL           string `json:"http_url_to_repo"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
}

func (p glProject) repo() Repo {
	return Repo{FullName: p.PathWithNamespace, Description: p.Description, Private: p.Visibility != "public", CloneURL: p.HTTPURL, DefaultBranch: p.DefaultBranch, WebURL: p.WebURL}
}

// ListRepos implements GitProvider.
func (g *GitLab) ListRepos(ctx context.Context, c Creds, query string) ([]Repo, error) {
	if err := checkHost(c); err != nil {
		return nil, err
	}
	var ps []glProject
	u := glBase(c) + "/api/v4/projects?membership=true&simple=false&per_page=100&order_by=last_activity_at"
	if query != "" {
		u += "&search=" + url.QueryEscape(query)
	}
	if _, err := call(ctx, g.HC, "GET", u, glHdr(c), nil, &ps); err != nil {
		return nil, err
	}
	var out []Repo
	for _, p := range ps {
		out = append(out, p.repo())
	}
	return out, nil
}

// CreateRepo implements GitProvider.
func (g *GitLab) CreateRepo(ctx context.Context, c Creds, n NewRepo) (Repo, error) {
	if err := checkHost(c); err != nil {
		return Repo{}, err
	}
	vis := "public"
	if n.Private {
		vis = "private"
	}
	var p glProject
	body := jsonBody(map[string]any{"name": n.Name, "description": n.Description, "visibility": vis, "initialize_with_readme": true})
	if _, err := call(ctx, g.HC, "POST", glBase(c)+"/api/v4/projects", glHdr(c), body, &p); err != nil {
		return Repo{}, err
	}
	return p.repo(), nil
}

// OpenPR implements GitProvider (a merge request).
func (g *GitLab) OpenPR(ctx context.Context, c Creds, pr PullRequest) (string, error) {
	if err := checkHost(c); err != nil {
		return "", err
	}
	if !validFullName(pr.Repo) {
		return "", fmt.Errorf("bad project name %q", pr.Repo)
	}
	id := escapePath(pr.Repo)
	base := pr.Base
	if base == "" {
		var p glProject
		if _, err := call(ctx, g.HC, "GET", glBase(c)+"/api/v4/projects/"+id, glHdr(c), nil, &p); err != nil {
			return "", err
		}
		base = p.DefaultBranch
	}
	var out struct {
		WebURL string `json:"web_url"`
	}
	body := jsonBody(map[string]any{"source_branch": pr.Head, "target_branch": base, "title": pr.Title, "description": pr.Body})
	if _, err := call(ctx, g.HC, "POST", glBase(c)+"/api/v4/projects/"+id+"/merge_requests", glHdr(c), body, &out); err != nil {
		return "", err
	}
	return out.WebURL, nil
}
