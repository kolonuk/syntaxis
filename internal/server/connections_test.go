// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/secrets"
)

type fakeGit struct {
	seen  connections.Creds
	prArg connections.PullRequest
}

func (f *fakeGit) Kind() connections.Kind {
	return connections.Kind{ID: "gitlab", Title: "GitLab", Git: true, Fields: []connections.Field{
		{Name: "GITLAB_TOKEN", Label: "Token", Secret: true, Required: true},
		{Name: "GITLAB_HOST", Label: "Address", Default: "https://gitlab.example.com"},
	}}
}
func (f *fakeGit) Test(_ context.Context, c connections.Creds) (connections.Identity, error) {
	f.seen = c
	if c["GITLAB_TOKEN"] != "good-token-123" {
		return connections.Identity{}, connections.ErrBadCredentials
	}
	return connections.Identity{Account: "gl-user"}, nil
}
func (f *fakeGit) ListRepos(_ context.Context, _ connections.Creds, q string) ([]connections.Repo, error) {
	return []connections.Repo{{FullName: "gl-user/app", CloneURL: "https://gitlab.example.com/gl-user/app.git"}}, nil
}
func (f *fakeGit) CreateRepo(_ context.Context, _ connections.Creds, n connections.NewRepo) (connections.Repo, error) {
	return connections.Repo{FullName: "gl-user/" + n.Name}, nil
}
func (f *fakeGit) OpenPR(_ context.Context, _ connections.Creds, pr connections.PullRequest) (string, error) {
	f.prArg = pr
	return "https://gitlab.example.com/mr/1", nil
}

func TestConnectionsArePerUserAndNeverReturnCredentials(t *testing.T) {
	e := newEnv(t)
	fg := &fakeGit{}
	e.app.Connections.Set(fg)
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")

	// a bad token is refused and nothing is stored
	if code, m, _ := alice.do("PUT", "/api/connections/gitlab", map[string]any{"fields": map[string]string{"GITLAB_TOKEN": "bad"}}); code != 400 || !strings.Contains(m["error"].(string), "did not work") {
		t.Fatalf("%d %v", code, m)
	}
	if names, _ := e.app.Secrets.Names(context.Background(), secrets.ScopeUser, userID(t, e, "alice@example.com")); len(names) != 0 {
		t.Fatalf("a failed connection stored %v", names)
	}

	if code, m, raw := alice.do("PUT", "/api/connections/gitlab", map[string]any{"fields": map[string]string{"GITLAB_TOKEN": "good-token-123"}}); code != 200 || m["account"] != "gl-user" {
		t.Fatalf("%d %s", code, raw)
	}
	if fg.seen["GITLAB_HOST"] != "https://gitlab.example.com" {
		t.Fatalf("the default address should be filled in: %v", fg.seen)
	}

	_, list, raw := alice.do("GET", "/api/connections", nil)
	_ = list
	if strings.Contains(string(raw), "good-token-123") {
		t.Fatal("the token was returned by the API")
	}
	var found bool
	for _, c := range decodeList(t, raw) {
		if c["kind"].(map[string]any)["id"] == "gitlab" {
			found = true
			if c["connected"] != true || c["account"] != "gl-user" || c["stored"].(map[string]any)["GITLAB_TOKEN"] != true {
				t.Fatalf("%v", c)
			}
		}
	}
	if !found {
		t.Fatal("gitlab missing from the list")
	}

	// bob sees nothing connected and cannot use alice's connection
	_, _, rawBob := bob.do("GET", "/api/connections", nil)
	for _, c := range decodeList(t, rawBob) {
		if c["kind"].(map[string]any)["id"] == "gitlab" && c["connected"] == true {
			t.Fatal("another user's connection is visible")
		}
	}
	if code, _, _ := bob.do("GET", "/api/connections/gitlab/repos", nil); code != 400 {
		t.Fatalf("bob has no connection: %d", code)
	}

	// the sandbox gets the token and the address; only the token is a redacted secret
	env, red, _ := e.app.Secrets.Snapshot(context.Background(), userID(t, e, "alice@example.com"))
	if env["GITLAB_TOKEN"] != "good-token-123" || env["GITLAB_HOST"] != "https://gitlab.example.com" {
		t.Fatalf("%v", env)
	}
	if red.String("token good-token-123 at https://gitlab.example.com") != "token [REDACTED] at https://gitlab.example.com" {
		t.Fatalf("%q", red.String("token good-token-123 at https://gitlab.example.com"))
	}
	if envB, _, _ := e.app.Secrets.Snapshot(context.Background(), userID(t, e, "bob@example.com")); envB["GITLAB_TOKEN"] != "" {
		t.Fatal("bob's sandbox received alice's token")
	}

	// repositories: list, create (with a safe name), refuse odd names
	if code, _, raw := alice.do("GET", "/api/connections/gitlab/repos", nil); code != 200 || !strings.Contains(string(raw), "gl-user/app") {
		t.Fatalf("%d %s", code, raw)
	}
	if code, m, _ := alice.do("POST", "/api/connections/gitlab/repos", map[string]any{"name": "../evil"}); code != 400 {
		t.Fatalf("%d %v", code, m)
	}
	if code, m, _ := alice.do("POST", "/api/connections/gitlab/repos", map[string]any{"name": "fresh", "private": true}); code != 200 || m["full_name"] != "gl-user/fresh" {
		t.Fatalf("%d %v", code, m)
	}

	// changing only the address keeps the stored token
	if code, _, raw := alice.do("PUT", "/api/connections/gitlab", map[string]any{"fields": map[string]string{"GITLAB_HOST": "https://other.example.com", "GITLAB_TOKEN": ""}}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if fg.seen["GITLAB_TOKEN"] != "good-token-123" || fg.seen["GITLAB_HOST"] != "https://other.example.com" {
		t.Fatalf("%v", fg.seen)
	}

	// unknown fields and services are refused; removing deletes the credentials
	if code, _, _ := alice.do("PUT", "/api/connections/gitlab", map[string]any{"fields": map[string]string{"NOT_A_FIELD": "x"}}); code != 400 {
		t.Fatal("unknown field accepted")
	}
	if code, _, _ := alice.do("PUT", "/api/connections/myspace", map[string]any{"fields": map[string]string{}}); code != 404 {
		t.Fatal("unknown service accepted")
	}
	if code, _, _ := alice.do("DELETE", "/api/connections/gitlab", nil); code != 200 {
		t.Fatal("delete")
	}
	env, _, _ = e.app.Secrets.Snapshot(context.Background(), userID(t, e, "alice@example.com"))
	if env["GITLAB_TOKEN"] != "" || env["GITLAB_HOST"] != "" {
		t.Fatalf("removed connection still injected: %v", env)
	}
}

func decodeList(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return out
}
