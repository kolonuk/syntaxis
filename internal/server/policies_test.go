// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func userID(t *testing.T, e *env, email string) int64 {
	t.Helper()
	us, _ := e.st.ListUsers(context.Background())
	for _, u := range us {
		if u.Email == email {
			return u.ID
		}
	}
	t.Fatalf("no user %s", email)
	return 0
}

func TestGroupAndLevelSettingsReachTheUser(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	u := e.userSession("a@example.com")
	id := userID(t, e, "a@example.com")

	// a group sets a daily token limit; the user has none of their own
	code, m, raw := adm.do("POST", "/api/groups", map[string]any{"name": "friends", "policy": map[string]any{"daily_tokens": 4242, "no_terminal": true}})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	gid := int64(m["id"].(float64))
	if code, _, raw := adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"group_id": gid}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, usage, _ := u.do("GET", "/api/usage", nil)
	if d := usage["daily"].(map[string]any); d["Limit"] != float64(4242) {
		t.Fatalf("the group's limit must apply: %v", usage)
	}
	// the group turns the terminal off
	e.app.Sandbox = sandbox.NewManager(sandbox.NewFake(), sandbox.DefaultConfig(), nil)
	seedModel(e)
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	if code, body, _ := u.do("GET", "/api/sessions/"+s["id"].(string)+"/terminal", nil); code != 403 || !strings.Contains(body["error"].(string), "turned the terminal off") {
		t.Fatalf("a group that disables the terminal must apply: %d %v", code, body)
	}

	// the user's own setting beats the group
	adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"quota_daily_tokens": 77})
	_, usage, _ = u.do("GET", "/api/usage", nil)
	if d := usage["daily"].(map[string]any); d["Limit"] != float64(77) {
		t.Fatalf("an explicit user setting must beat the group: %v", usage)
	}

	// a level sets what neither the user nor the group sets
	adm.do("PUT", "/api/levels/poweruser", map[string]any{"monthly_tokens": 999999})
	adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"level": "poweruser"})
	_, usage, _ = u.do("GET", "/api/usage", nil)
	if mo := usage["monthly"].(map[string]any); mo["Limit"] != float64(999999) {
		t.Fatalf("the level's monthly limit must apply: %v", usage)
	}

	// deleting the group returns the user to no group
	if code, _, _ := adm.do("DELETE", "/api/groups/"+itoa(gid), nil); code != 200 {
		t.Fatal("delete")
	}
	got, _ := e.st.GetUser(context.Background(), id)
	if got.GroupID != 0 {
		t.Fatalf("group not cleared: %d", got.GroupID)
	}
}

func TestOnlyARealAdminCanBeGivenTheAdminLevel(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	e.userSession("stranger@example.com")
	id := userID(t, e, "stranger@example.com")
	if code, m, _ := adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"level": "admin"}); code != 400 || !strings.Contains(m["error"].(string), "no admin account") {
		t.Fatalf("the admin level must need a matching admin account: %d %v", code, m)
	}
	if code, _, _ := adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"level": "emperor"}); code != 400 {
		t.Fatal("an unknown level was accepted")
	}
	if code, _, _ := adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"group_id": 9999}); code != 400 {
		t.Fatal("a missing group was accepted")
	}
}

func TestAdminModeLinkAndBackLink(t *testing.T) {
	e := newEnv(t)
	e.app.Cfg.AdminURL = "http://admin.example.com:8081"
	e.app.Cfg.BaseURL = "https://chat.example.com"
	adm := e.makeAdminSession()
	// the admin's own chat account carries the admin level
	if _, me, _ := adm.do("GET", "/api/me", nil); me["chat_url"] != "https://chat.example.com" {
		t.Fatalf("the admin app should link back to the chat app: %v", me)
	}
	u := e.userSession("boss@example.com") // the admin's own chat account: same email as the admin
	if _, me, _ := u.do("GET", "/api/me", nil); me["admin_url"] != nil {
		t.Fatalf("an ordinary user must never be shown the admin link: %v", me)
	}
	id := userID(t, e, "boss@example.com")
	if code, _, raw := adm.do("PATCH", "/api/users/"+itoa(id), map[string]any{"level": "admin"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if _, me, _ := u.do("GET", "/api/me", nil); me["admin_url"] != "http://admin.example.com:8081" || me["level"] != store.LevelAdmin {
		t.Fatalf("an admin-level user gets the link: %v", me)
	}
}
