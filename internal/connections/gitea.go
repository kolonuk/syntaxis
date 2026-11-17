// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Gitea connects a user's account on a Gitea (or Forgejo) server.
type Gitea struct{ HC *http.Client }

func gtBase(c Creds) string { return strings.TrimRight(strings.TrimSpace(c["GITEA_URL"]), "/") }

func gtHdr(c Creds) map[string]string {
	return map[string]string{"Authorization": "token " + c["GITEA_TOKEN"], "Accept": "application/json", "Content-Type": "application/json"}
}

func gtCheck(c Creds) error {
	if h := gtBase(c); !strings.HasPrefix(h, "https://") && !strings.HasPrefix(h, "http://") {
		return fmt.Errorf("the server address must start with https://")
	}
	return nil
}

// Kind implements Provider.
func (g *Gitea) Kind() Kind {
	return Kind{ID: "gitea", Title: "Gitea / Forgejo", Git: true,
		Guide: "On your Gitea server open Settings, Applications, and create an access token with read and write access to repository (and write:issue if the agent should open pull requests). Enter the server's address below.",
		Fields: []Field{
			{Name: "GITEA_TOKEN", Label: "Access token", Secret: true, Required: true},
			{Name: "GITEA_URL", Label: "Server address", Hint: "https://gitea.example.com", Required: true, URL: true},
		}}
}

// Test implements Provider.
func (g *Gitea) Test(ctx context.Context, c Creds) (Identity, error) {
	if err := gtCheck(c); err != nil {
		return Identity{}, err
	}
	var u struct {
		Login    string `json:"login"`
		FullName string `json:"full_name"`
	}
	if _, err := call(ctx, g.HC, "GET", gtBase(c)+"/api/v1/user", gtHdr(c), nil, &u); err != nil {
		return Identity{}, err
	}
	return Identity{Account: u.Login, Detail: u.FullName}, nil
}

type gtRepo struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

func (r gtRepo) repo() Repo {
	return Repo{FullName: r.FullName, Description: r.Description, Private: r.Private, CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, WebURL: r.HTMLURL}
}

// ListRepos implements GitProvider.
func (g *Gitea) ListRepos(ctx context.Context, c Creds, query string) ([]Repo, error) {
	if err := gtCheck(c); err != nil {
		return nil, err
	}
	var rs []gtRepo
	if _, err := call(ctx, g.HC, "GET", gtBase(c)+"/api/v1/user/repos?limit=100", gtHdr(c), nil, &rs); err != nil {
		return nil, err
	}
	return filterRepos(rs, query, func(r gtRepo) Repo { return r.repo() }), nil
}

// CreateRepo implements GitProvider.
func (g *Gitea) CreateRepo(ctx context.Context, c Creds, n NewRepo) (Repo, error) {
	if err := gtCheck(c); err != nil {
		return Repo{}, err
	}
	var r gtRepo
	body := jsonBody(map[string]any{"name": n.Name, "description": n.Description, "private": n.Private, "auto_init": true})
	if _, err := call(ctx, g.HC, "POST", gtBase(c)+"/api/v1/user/repos", gtHdr(c), body, &r); err != nil {
		return Repo{}, err
	}
	return r.repo(), nil
}

// ReadFile implements GitProvider.
func (g *Gitea) ReadFile(ctx context.Context, c Creds, repo, ref, path string) ([]byte, error) {
	if err := gtCheck(c); err != nil {
		return nil, err
	}
	if !validFullName(repo) || !safeRepoPath(path) || !safeRef(ref) {
		return nil, fmt.Errorf("bad repository, path or branch")
	}
	u := gtBase(c) + "/api/v1/repos/" + repo + "/raw/" + path
	if ref != "" {
		u += "?ref=" + ref
	}
	return callRaw(ctx, g.HC, u, gtHdr(c))
}

// OpenPR implements GitProvider.
func (g *Gitea) OpenPR(ctx context.Context, c Creds, pr PullRequest) (string, error) {
	if err := gtCheck(c); err != nil {
		return "", err
	}
	if !validFullName(pr.Repo) {
		return "", fmt.Errorf("bad repository name %q", pr.Repo)
	}
	base := pr.Base
	if base == "" {
		var r gtRepo
		if _, err := call(ctx, g.HC, "GET", gtBase(c)+"/api/v1/repos/"+pr.Repo, gtHdr(c), nil, &r); err != nil {
			return "", err
		}
		base = r.DefaultBranch
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	body := jsonBody(map[string]any{"title": pr.Title, "body": pr.Body, "head": pr.Head, "base": base})
	if _, err := call(ctx, g.HC, "POST", gtBase(c)+"/api/v1/repos/"+pr.Repo+"/pulls", gtHdr(c), body, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}
