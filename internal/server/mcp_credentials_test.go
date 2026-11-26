// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/mcp"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/store"
)

// These tests keep an MCP server's credentials strictly the running user's own: an instance-wide
// secret is never sent to a server, one user's value never reaches another user's run, and a user
// cannot switch a server on without supplying their own credentials or without the admin's approval.

func mcpCredEnv(t *testing.T) (*env, *client, *client, int64, int64) {
	t.Helper()
	e := newEnv(t)
	e.app.MCPSource = mcp.Local{Entries: []mcp.Entry{{
		Name: "io.example/notes-server", Title: "Notes", Description: "keeps notes", Status: "active",
		Packages: []mcp.Package{{RegistryType: "npm", Identifier: "notes-mcp", Version: "1.0.0", Transport: "stdio",
			Env: []mcp.EnvVar{{Name: "NOTES_TOKEN", Required: true, Secret: true}}}},
	}}}
	ann := e.userSession("ann-cred@example.com")
	bob := e.userSession("bob-cred@example.com")
	id := func(email string) int64 {
		a, err := e.st.GetAccountByEmail(t.Context(), store.KindUser, email)
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	return e, ann, bob, id("ann-cred@example.com"), id("bob-cred@example.com")
}

func approveNotes(t *testing.T, e *env, creds map[string]string) {
	t.Helper()
	adm := e.makeAdminSession()
	if code, m, raw := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/notes-server", "package": 0, "env_secrets": creds}); code != 200 {
		t.Fatalf("approve: %d %v %s", code, m, raw)
	}
}

func notesApproval(t *testing.T, e *env) mcp.Approval {
	t.Helper()
	l, err := e.app.mcpApprovals().List(t.Context())
	if err != nil || len(l) != 1 {
		t.Fatalf("%v %v", l, err)
	}
	return l[0]
}

func TestAnInstanceSecretIsNeverGivenToAnMCPServer(t *testing.T) {
	e, ann, _, annID, _ := mcpCredEnv(t)
	// an admin-owned secret with the very name the server asks for
	if err := e.app.Secrets.Put(t.Context(), secrets.ScopeInstance, 0, "NOTES_TOKEN", "ADMIN-SHARED-VALUE"); err != nil {
		t.Fatal(err)
	}
	approveNotes(t, e, map[string]string{"NOTES_TOKEN": "NOTES_TOKEN"})
	ap := notesApproval(t, e)

	// the user has supplied nothing: the shared value must not fill the gap
	vals, missing := e.app.mcpCredentials(t.Context(), annID, ap)
	if len(vals) != 0 || len(missing) != 1 || missing[0] != "NOTES_TOKEN" {
		t.Fatalf("an instance secret filled in for a user: %v %v", vals, missing)
	}
	// once the user supplies their own, that is what the server gets, and only that
	if code, _, raw := ann.do("PUT", "/api/secrets/NOTES_TOKEN", map[string]string{"value": "ann-own-value"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	vals, missing = e.app.mcpCredentials(t.Context(), annID, ap)
	if len(missing) != 0 || vals["NOTES_TOKEN"] != "ann-own-value" {
		t.Fatalf("%v %v", vals, missing)
	}
	spec, err := mcp.ContainerSpec(sandbox.DefaultConfig(), ap, vals, mcp.Owner{UserID: annID})
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(spec.Env, " ")
	if !strings.Contains(env, "NOTES_TOKEN=ann-own-value") || strings.Contains(env, "ADMIN-SHARED-VALUE") {
		t.Fatalf("the server's environment: %s", env)
	}
}

func TestOneUsersCredentialNeverReachesAnotherUsersServer(t *testing.T) {
	e, ann, bob, annID, bobID := mcpCredEnv(t)
	approveNotes(t, e, map[string]string{"NOTES_TOKEN": "NOTES_TOKEN"})
	ap := notesApproval(t, e)
	if code, _, _ := ann.do("PUT", "/api/secrets/NOTES_TOKEN", map[string]string{"value": "ann-own-value"}); code != 200 {
		t.Fatal("ann's secret")
	}
	// bob has nothing, so he has nothing to run with
	if vals, missing := e.app.mcpCredentials(t.Context(), bobID, ap); len(vals) != 0 || len(missing) != 1 {
		t.Fatalf("bob received %v (missing %v)", vals, missing)
	}
	// and he cannot switch the server on until he supplies his own
	if code, m, _ := bob.do("PUT", "/api/mcp", map[string]any{"name": "io.example/notes-server", "enabled": true}); code != 409 || !strings.Contains(m["error"].(string), "NOTES_TOKEN") {
		t.Fatalf("bob switched on a server without his own credential: %d %v", code, m)
	}
	_, _, raw := bob.do("GET", "/api/mcp", nil)
	if strings.Contains(string(raw), "ann-own-value") || !strings.Contains(string(raw), `"ready":false`) {
		t.Fatalf("bob's list: %s", raw)
	}
	if code, _, _ := bob.do("PUT", "/api/secrets/NOTES_TOKEN", map[string]string{"value": "bob-own-value"}); code != 200 {
		t.Fatal("bob's secret")
	}
	if code, _, raw := bob.do("PUT", "/api/mcp", map[string]any{"name": "io.example/notes-server", "enabled": true}); code != 200 {
		t.Fatalf("bob with his own credential: %d %s", code, raw)
	}
	bv, _ := e.app.mcpCredentials(t.Context(), bobID, ap)
	av, _ := e.app.mcpCredentials(t.Context(), annID, ap)
	if bv["NOTES_TOKEN"] != "bob-own-value" || av["NOTES_TOKEN"] != "ann-own-value" {
		t.Fatalf("bob %v ann %v", bv, av)
	}
	// the value is never shown back: the list names the credential and whether it is set
	_, _, raw = bob.do("GET", "/api/mcp", nil)
	if strings.Contains(string(raw), "bob-own-value") || !strings.Contains(string(raw), `"set":true`) {
		t.Fatalf("a credential value was returned or the state is wrong: %s", raw)
	}
}

func TestAUserCannotSwitchOnAServerTheAdminHasNotApproved(t *testing.T) {
	e, ann, _, _, _ := mcpCredEnv(t)
	for _, name := range []string{"io.example/notes-server", "io.example/anything"} {
		if code, _, _ := ann.do("PUT", "/api/mcp", map[string]any{"name": name, "enabled": true}); code != 404 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	// asking for it is not approving it
	if code, _, _ := ann.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/notes-server"}); code != 201 {
		t.Fatal("request")
	}
	if code, _, _ := ann.do("PUT", "/api/mcp", map[string]any{"name": "io.example/notes-server", "enabled": true}); code != 404 {
		t.Fatalf("a request was treated as an approval: %d", code)
	}
	// the user's listener has no way to approve
	if code, _, _ := ann.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/notes-server", "package": 0}); code == 200 {
		t.Fatal("a user approved a server")
	}
	if l, _ := e.app.mcpApprovals().List(t.Context()); len(l) != 0 {
		t.Fatalf("approvals: %v", l)
	}
}

// The admin names only which credential each variable takes. A credential name that is not a plain
// name is refused, and the old routes for shared (instance) secrets are gone.
func TestTheAdminCannotMapAServerToASharedSecret(t *testing.T) {
	e, _, _, _, _ := mcpCredEnv(t)
	adm := e.makeAdminSession()
	if code, _, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/notes-server", "package": 0, "env_secrets": map[string]string{"NOTES_TOKEN": "../etc"}}); code != 400 {
		t.Fatalf("a bad credential name: %d", code)
	}
	if code, _, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/notes-server", "package": 0, "env_secrets": map[string]string{"not a variable": "X"}}); code != 400 {
		t.Fatalf("a bad variable name: %d", code)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/api/secrets"}, {"PUT", "/api/secrets/X"}, {"DELETE", "/api/secrets/X"}} {
		if code, _, _ := adm.do(c.method, c.path, map[string]string{"value": "v"}); code != 404 && code != 405 {
			t.Fatalf("%s %s still exists on the admin listener: %d", c.method, c.path, code)
		}
	}
	// left unmapped, the credential takes the variable's own name
	approveNotes(t, e, nil)
	if got := notesApproval(t, e).EnvSecrets["NOTES_TOKEN"]; got != "NOTES_TOKEN" {
		t.Fatalf("%q", got)
	}
}

// An approval that used a shared secret before each user had their own is converted once, the admin is
// told which variables it affected, and the note goes away when dismissed.
func TestAdminIsToldOnceAboutServersThatUsedASharedSecret(t *testing.T) {
	e := newEnv(t)
	legacy := `{"old":{"name":"old","package":{"registryType":"npm","identifier":"x","version":"1.0.0","transport":"stdio"},"env_secrets":{"KEY":"SHARED_ADMIN_KEY","TOKEN":"user:GITHUB_TOKEN"},"enabled":true}}`
	if err := e.st.SetSetting(t.Context(), "mcp_approvals", legacy); err != nil {
		t.Fatal(err)
	}
	adm := e.makeAdminSession()
	_, _, raw := adm.do("GET", "/api/mcp/approved", nil)
	if !strings.Contains(string(raw), `"shared_before":["KEY"]`) || !strings.Contains(string(raw), `"TOKEN":"GITHUB_TOKEN"`) || !strings.Contains(string(raw), `"KEY":"KEY"`) {
		t.Fatalf("the migrated approval: %s", raw)
	}
	if strings.Contains(string(raw), "SHARED_ADMIN_KEY") {
		t.Fatalf("the shared secret name is still mapped: %s", raw)
	}
	if code, _, _ := adm.do("DELETE", "/api/mcp/shared-notice", nil); code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
	if _, _, raw = adm.do("GET", "/api/mcp/approved", nil); strings.Contains(string(raw), "shared_before\":[") {
		t.Fatalf("the note is still there: %s", raw)
	}
	// a chat user cannot dismiss it
	usr := e.userSession("notice-user@example.com")
	if code, _, _ := usr.do("DELETE", "/api/mcp/shared-notice", nil); code == 200 {
		t.Fatal("a user dismissed the admin's note")
	}
}
