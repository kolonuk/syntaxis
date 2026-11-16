// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kolonuk/syntaxis/internal/connections"
)

// workBranch is the branch the agent commits to for a session, so the user's own branches are
// never touched.
func workBranch(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
		if b.Len() >= 8 {
			break
		}
	}
	return "syntaxis/" + b.String()
}

// expectedHost is the host a clone address for this service must have.
func expectedHost(kind string, creds connections.Creds) string {
	switch kind {
	case "github":
		return "github.com"
	case "gitlab":
		h := creds["GITLAB_HOST"]
		if h == "" {
			h = "https://gitlab.com"
		}
		u, err := url.Parse(h)
		if err != nil {
			return ""
		}
		return u.Host
	case "gitea":
		u, err := url.Parse(creds["GITEA_URL"])
		if err != nil {
			return ""
		}
		return u.Host
	}
	return ""
}

// checkCloneURL accepts a clone address only if it is on the service the user connected, in the
// shape https://host/owner/name(.git) with no embedded credentials, so a session can only ever
// clone what the connection's own service says it is.
func checkCloneURL(raw, fullName, host string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("that repository address is not valid")
	}
	if host == "" || !strings.EqualFold(u.Host, host) {
		return errors.New("that repository is not on the service you connected")
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if !strings.EqualFold(p, fullName) {
		return errors.New("the repository address does not match its name")
	}
	return nil
}

// resolveRepo checks a repository the user picked, or creates the one they asked for, using their
// own connection. It returns the repository to put on the session.
func (a *App) resolveRepo(ctx context.Context, userID int64, kind, fullName, cloneURL, branch string, create *connections.NewRepo) (connections.Repo, error) {
	g, ok := a.Connections.Git(kind)
	if !ok {
		return connections.Repo{}, errors.New("choose GitHub, GitLab or Gitea")
	}
	creds := a.credsFor(ctx, userID, g.Kind())
	if missingRequired(g.Kind(), creds) {
		return connections.Repo{}, fmt.Errorf("connect %s first (Settings, Connections)", g.Kind().Title)
	}
	if create != nil {
		create.Name = strings.TrimSpace(create.Name)
		if !validRepoName(create.Name) {
			return connections.Repo{}, errors.New("a repository name uses letters, digits, dashes, dots and underscores")
		}
		repo, err := g.CreateRepo(ctx, creds, *create)
		if err != nil {
			return connections.Repo{}, fmt.Errorf("could not create the repository: %w", err)
		}
		fullName, cloneURL, branch = repo.FullName, repo.CloneURL, repo.DefaultBranch
	}
	if !validFullName(fullName) {
		return connections.Repo{}, errors.New("a repository is owner/name")
	}
	if err := checkCloneURL(cloneURL, fullName, expectedHost(kind, creds)); err != nil {
		return connections.Repo{}, err
	}
	if branch == "" || strings.ContainsAny(branch, " \t\n;|&$`") || strings.HasPrefix(branch, "-") {
		branch = "main"
	}
	return connections.Repo{FullName: fullName, CloneURL: cloneURL, DefaultBranch: branch}, nil
}

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
