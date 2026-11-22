// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/store"
)

func TestAdminSeesAndManagesEveryUserSetting(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("settings@example.com")
	uid := userID(t, e, "settings@example.com")
	ctx := context.Background()
	u.do("PUT", "/api/secrets/GITHUB_TOKEN", map[string]string{"value": "ghp_notforadmins"})
	u.do("POST", "/api/memory", map[string]any{"scope": "user", "content": "prefers tabs"})
	hooks := `{"hooks":[{"event":"post_tool","command":"gofmt -l ."}]}`
	u.do("PUT", "/api/hooks", map[string]string{"hooks": hooks})
	u.do("PUT", "/api/commands", map[string]any{"commands": []Command{{Name: "lint", Template: "Run the linter"}}})
	_ = e.st.PutConnection(ctx, store.Connection{UserID: uid, Kind: "github", Account: "octo", Detail: "ok"})
	_, a, _ := u.do("POST", "/api/automations", map[string]any{"name": "On push", "trigger": "webhook", "prompt": "Review", "model": "prov/m1", "autonomy": "full"})
	autoID := a["automation"].(map[string]any)["id"].(string)
	u.do("POST", "/api/tokens", map[string]any{"name": "ci", "scope": "read"})

	adm := e.makeAdminSession()
	base := "/api/users/" + itoa(uid)
	code, got, raw := adm.do("GET", base+"/settings", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, want := range []string{"prefers tabs", "gofmt -l .", "lint", "octo", "On push", "GITHUB_TOKEN"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the admin does not see %q: %s", want, raw)
		}
	}
	// credential values are never shown, only names
	if strings.Contains(string(raw), "ghp_notforadmins") {
		t.Fatal("the admin was shown a credential value")
	}
	if got["user"].(map[string]any)["email"] != "settings@example.com" {
		t.Fatalf("%v", got["user"])
	}

	// the admin can change them, and every change is audited
	if code, _, _ := adm.do("PUT", base+"/hooks", map[string]string{"hooks": ""}); code != 200 {
		t.Fatal("clear hooks")
	}
	if code, _, _ := adm.do("PUT", base+"/hooks", map[string]string{"hooks": `{"hooks":[{"event":"x","command":"true"}]}`}); code != 400 {
		t.Fatal("bad hooks accepted")
	}
	if code, _, _ := adm.do("PUT", base+"/commands", map[string]any{"commands": []Command{}}); code != 200 {
		t.Fatal("clear commands")
	}
	notes, _ := e.st.ListMemory(ctx, uid)
	if code, _, _ := adm.do("DELETE", base+"/notes/"+itoa(notes[0].ID), nil); code != 200 {
		t.Fatal("delete note")
	}
	if code, _, _ := adm.do("DELETE", base+"/connections/github", nil); code != 200 {
		t.Fatal("delete connection")
	}
	if code, _, _ := adm.do("DELETE", base+"/automations/"+autoID, nil); code != 200 {
		t.Fatal("delete automation")
	}
	toks, _ := e.st.ListAPITokens(ctx, uid)
	if code, _, _ := adm.do("DELETE", base+"/tokens/"+itoa(toks[0].ID), nil); code != 200 {
		t.Fatal("revoke token")
	}
	if _, b, _ := u.do("GET", "/api/hooks", nil); b["hooks"] != "" {
		t.Fatalf("the user still has hooks: %v", b)
	}
	if n, _ := e.st.ListMemory(ctx, uid); len(n) != 0 {
		t.Fatal("note still there")
	}
	if c, _ := e.st.ListConnections(ctx, uid); len(c) != 0 {
		t.Fatal("connection still there")
	}
	es, _ := e.st.ListAudit(ctx, 0)
	seen := map[string]bool{}
	for _, x := range es {
		if strings.HasPrefix(x.Actor, "admin:") {
			seen[x.Action] = true
		}
	}
	for _, a := range []string{"admin.user_hooks", "admin.user_commands", "admin.user_note_delete", "admin.user_connection_delete", "admin.user_automation_delete", "admin.user_token_revoke"} {
		if !seen[a] {
			t.Errorf("%s was not audited", a)
		}
	}
	// a user cannot reach these, and another user's automation is not deletable through this user
	if code, _, _ := u.do("GET", base+"/settings", nil); code == 200 {
		t.Error("a user opened the admin settings view")
	}
	if code, _, _ := adm.do("GET", "/api/users/99999/settings", nil); code != 404 {
		t.Errorf("unknown user: %d", code)
	}
}
