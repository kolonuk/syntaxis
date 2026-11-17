// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// Connections against a real Gitea server in a container: per user, checked when saved, and the
// credentials are never returned.
func TestConnectionsArePerUserAndNeverReturnCredentials(t *testing.T) {
	gs := testenv.Gitea(t)
	e := newEnv(t)
	allowLoopbackConnections(e) // the test Gitea listens on loopback
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")
	aliceID := userID(t, e, "alice@example.com")

	// a bad token is refused and nothing is stored
	if code, m, _ := alice.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": "bad", "GITEA_URL": gs.URL}}); code != 400 || !strings.Contains(m["error"].(string), "did not work") {
		t.Fatalf("%d %v", code, m)
	}
	if names, _ := e.app.Secrets.Names(context.Background(), secrets.ScopeUser, aliceID); len(names) != 0 {
		t.Fatalf("a failed connection stored %v", names)
	}

	if code, m, raw := alice.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": gs.Token, "GITEA_URL": gs.URL}}); code != 200 || m["account"] != gs.User {
		t.Fatalf("%d %s", code, raw)
	}
	_, _, raw := alice.do("GET", "/api/connections", nil)
	if strings.Contains(string(raw), gs.Token) {
		t.Fatal("the token was returned by the API")
	}
	var found bool
	for _, c := range decodeList(t, raw) {
		if c["kind"].(map[string]any)["id"] == "gitea" {
			found = true
			if c["connected"] != true || c["account"] != gs.User || c["stored"].(map[string]any)["GITEA_TOKEN"] != true {
				t.Fatalf("%v", c)
			}
		}
	}
	if !found {
		t.Fatal("gitea missing from the list")
	}

	// bob sees nothing connected and cannot use alice's connection
	_, _, rawBob := bob.do("GET", "/api/connections", nil)
	for _, c := range decodeList(t, rawBob) {
		if c["kind"].(map[string]any)["id"] == "gitea" && c["connected"] == true {
			t.Fatal("another user's connection is visible")
		}
	}
	if code, _, _ := bob.do("GET", "/api/connections/gitea/repos", nil); code != 400 {
		t.Fatalf("bob has no connection: %d", code)
	}

	// the sandbox gets the token and the address; only the token is a redacted secret
	env, red, _ := e.app.Secrets.Snapshot(context.Background(), aliceID)
	if env["GITEA_TOKEN"] != gs.Token || env["GITEA_URL"] != gs.URL {
		t.Fatalf("%v", env)
	}
	line := "token " + gs.Token + " at " + gs.URL
	if got := red.String(line); got != "token [REDACTED] at "+gs.URL {
		t.Fatalf("%q", got)
	}
	if envB, _, _ := e.app.Secrets.Snapshot(context.Background(), userID(t, e, "bob@example.com")); envB["GITEA_TOKEN"] != "" {
		t.Fatal("bob's sandbox received alice's token")
	}

	// repositories: create (with a safe name), list, refuse odd names
	if code, m, _ := alice.do("POST", "/api/connections/gitea/repos", map[string]any{"name": "../evil"}); code != 400 {
		t.Fatalf("%d %v", code, m)
	}
	if code, m, _ := alice.do("POST", "/api/connections/gitea/repos", map[string]any{"name": "fresh", "private": true}); code != 200 || m["full_name"] != gs.User+"/fresh" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _, raw := alice.do("GET", "/api/connections/gitea/repos", nil); code != 200 || !strings.Contains(string(raw), gs.User+"/fresh") {
		t.Fatalf("%d %s", code, raw)
	}

	// changing only the address keeps the stored token (the same server by another name)
	alt := strings.Replace(gs.URL, "127.0.0.1", "localhost", 1)
	if code, _, raw := alice.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_URL": alt, "GITEA_TOKEN": ""}}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	env, _, _ = e.app.Secrets.Snapshot(context.Background(), aliceID)
	if env["GITEA_TOKEN"] != gs.Token || env["GITEA_URL"] != alt {
		t.Fatalf("%v", env)
	}

	// unknown fields and services are refused; removing deletes the credentials
	if code, _, _ := alice.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"NOT_A_FIELD": "x"}}); code != 400 {
		t.Fatal("unknown field accepted")
	}
	if code, _, _ := alice.do("PUT", "/api/connections/myspace", map[string]any{"fields": map[string]string{}}); code != 404 {
		t.Fatal("unknown service accepted")
	}
	if code, _, _ := alice.do("DELETE", "/api/connections/gitea", nil); code != 200 {
		t.Fatal("delete")
	}
	env, _, _ = e.app.Secrets.Snapshot(context.Background(), aliceID)
	if env["GITEA_TOKEN"] != "" || env["GITEA_URL"] != "" {
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

// A user types the address of their self-hosted forge and the server calls it. They must not be
// able to aim it at the server's own admin app on loopback and read the reply.
func TestConnectionAddressCannotReachTheServersOwnServices(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	code, m, raw := u.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": "x", "GITEA_URL": e.admin.URL}})
	if code == 200 || strings.Contains(string(raw), "<html") || !strings.Contains(m["error"].(string), "not allowed") {
		t.Fatalf("the server called its own admin listener for a user: %d %s", code, raw)
	}
	if code, _, _ := u.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": "x", "GITEA_URL": "http://docker-socket-proxy:2375/containers/json#"}}); code != 400 {
		t.Fatalf("an address with a fragment was accepted: %d", code)
	}
}
