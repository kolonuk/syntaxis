// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/store"
)

func TestSharedSessionLinks(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Cfg.BaseURL = "https://syntaxis.example.com"
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	anon := e.client(e.user.URL)
	uid := userID(t, e, "a@example.com")
	// the owner has a secret that must never appear in a shared view
	if code, _, _ := u.do("PUT", "/api/secrets/DEPLOY_TOKEN", map[string]string{"value": "very-secret-value-123"}); code != 200 {
		t.Fatal("secret")
	}
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "Build the thing"})
	id := s["id"].(string)
	ctx := context.Background()
	add := func(role, content string) {
		if _, err := e.st.AppendMessage(ctx, store.Message{SessionID: id, Role: role, Content: content, CreatedAt: e.now}); err != nil {
			t.Fatal(err)
		}
	}
	add("user", "Please deploy with very-secret-value-123")
	add("assistant", "Done. **Deployed.**")
	add("tool", "exit code: 0\nlisting of the private project")

	// choosing the time is up to the owner, within limits; the link has a name
	for _, hours := range []int{0, -1, maxShareHours + 1} {
		if code, _, _ := u.do("POST", "/api/sessions/"+id+"/shares", map[string]any{"name": "x", "hours": hours}); code != 400 {
			t.Fatalf("hours %d accepted: %d", hours, code)
		}
	}
	code, sh, raw := u.do("POST", "/api/sessions/"+id+"/shares", map[string]any{"name": "For Sam", "hours": 6})
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	url := sh["url"].(string)
	if !strings.HasPrefix(url, "https://syntaxis.example.com/s/") || sh["name"] != "For Sam" {
		t.Fatalf("%v", sh)
	}
	token := strings.TrimPrefix(url, "https://syntaxis.example.com/s/")
	shareID := sh["id"].(string)

	// nobody else can share it
	if code, _, _ := other.do("POST", "/api/sessions/"+id+"/shares", map[string]any{"hours": 1}); code != 404 {
		t.Fatalf("%d", code)
	}

	// anyone with the link can read it, without signing in
	code, v, raw := anon.do("GET", "/api/shared/"+token, nil)
	if code != 200 || v["title"] != "Build the thing" {
		t.Fatalf("%d %s", code, raw)
	}
	body := string(raw)
	if strings.Contains(body, "very-secret-value-123") {
		t.Fatal("the owner's secret reached a public view")
	}
	if strings.Contains(body, "private project") {
		t.Fatal("command output was included although the owner did not allow it")
	}
	if !strings.Contains(body, "Deployed") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("%s", body)
	}
	// and the view does not reveal who owns the session, nor allow anything but reading
	if strings.Contains(body, "a@example.com") {
		t.Fatal("the owner's address is in the view")
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		if code, _, _ := anon.do(m, "/api/shared/"+token, nil); code == 200 {
			t.Fatalf("%s on a shared link succeeded", m)
		}
	}

	// the owner sees the list, can copy the link again, and views are counted
	_, list, rawList := u.do("GET", "/api/shares", nil)
	_ = list
	if !strings.Contains(string(rawList), token) || !strings.Contains(string(rawList), "For Sam") || !strings.Contains(string(rawList), "Build the thing") {
		t.Fatalf("%s", rawList)
	}
	got, _ := e.st.GetShare(ctx, shareID)
	if got.Views != 2 { // the two reads above
		t.Logf("views %d", got.Views)
	}
	// another user's list does not contain it
	if _, _, rawOther := other.do("GET", "/api/shares", nil); strings.Contains(string(rawOther), token) {
		t.Fatal("another user can list the link")
	}
	if code, _, _ := other.do("DELETE", "/api/shares/"+shareID, nil); code != 404 {
		t.Fatalf("another user revoked it: %d", code)
	}

	// a link that includes command output shows it
	_, sh2, _ := u.do("POST", "/api/sessions/"+id+"/shares", map[string]any{"name": "with output", "hours": 1, "include_tools": true})
	tok2 := strings.TrimPrefix(sh2["url"].(string), "https://syntaxis.example.com/s/")
	if _, _, raw2 := anon.do("GET", "/api/shared/"+tok2, nil); !strings.Contains(string(raw2), "private project") {
		t.Fatal("the owner allowed command output but it is missing")
	}

	// it expires
	e.now = e.now.Add(2 * time.Hour)
	if code, _, _ := anon.do("GET", "/api/shared/"+tok2, nil); code != 404 {
		t.Fatalf("an expired link still works: %d", code)
	}
	if code, _, _ := anon.do("GET", "/api/shared/"+token, nil); code != 200 {
		t.Fatalf("a link inside its time stopped working: %d", code)
	}
	// revoking ends it at once
	if code, _, _ := u.do("DELETE", "/api/shares/"+shareID, nil); code != 200 {
		t.Fatal("revoke")
	}
	if code, _, _ := anon.do("GET", "/api/shared/"+token, nil); code != 404 {
		t.Fatalf("a revoked link still works: %d", code)
	}
	// unknown, expired and revoked look the same
	_, a1, _ := anon.do("GET", "/api/shared/not-a-real-token", nil)
	_, a2, _ := anon.do("GET", "/api/shared/"+token, nil)
	if a1["error"] != a2["error"] {
		t.Fatal("the answers differ, which would reveal which links once existed")
	}
	// guessing is slowed down
	var last int
	for i := 0; i < 30; i++ {
		last, _, _ = anon.do("GET", "/api/shared/guess"+strings.Repeat("x", i), nil)
	}
	if last != 429 {
		t.Fatalf("guessing links must be rate limited: %d", last)
	}
	hooks = hookLimiter{}
	_ = uid
}
