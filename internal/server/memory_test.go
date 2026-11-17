// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/agent"
)

func TestMemoryAPI(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("m1@example.com")
	other := e.userSession("m2@example.com")
	if code, _, _ := e.client(e.user.URL).do("GET", "/api/memory", nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	code, b, _ := u.do("POST", "/api/memory", map[string]string{"scope": "user", "content": "  prefers tabs "})
	if code != 201 {
		t.Fatalf("%d %v", code, b)
	}
	id := int64(b["id"].(float64))
	if code, _, _ := u.do("POST", "/api/memory", map[string]string{"scope": "repo", "repo": "acme/app", "content": "tests: make check"}); code != 201 {
		t.Fatal("repo note")
	}
	for name, body := range map[string]map[string]string{
		"empty":        {"scope": "user", "content": " "},
		"long":         {"scope": "user", "content": strings.Repeat("x", agent.MaxNoteLength+1)},
		"bad scope":    {"scope": "all", "content": "x"},
		"repo no name": {"scope": "repo", "content": "x"},
	} {
		if code, _, _ := u.do("POST", "/api/memory", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	_, _, raw := u.do("GET", "/api/memory", nil)
	if !strings.Contains(string(raw), "prefers tabs") || !strings.Contains(string(raw), `"repo":"acme/app"`) {
		t.Fatalf("%s", raw)
	}
	// another user sees none of it and cannot change or delete it
	if _, _, raw := other.do("GET", "/api/memory", nil); strings.Contains(string(raw), "tabs") {
		t.Fatalf("notes leaked: %s", raw)
	}
	if code, _, _ := other.do("PUT", fmt.Sprintf("/api/memory/%d", id), map[string]string{"content": "x"}); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, _, _ := other.do("DELETE", fmt.Sprintf("/api/memory/%d", id), nil); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, _, _ := u.do("PUT", fmt.Sprintf("/api/memory/%d", id), map[string]string{"content": "prefers spaces"}); code != 200 {
		t.Fatal("edit")
	}
	if _, _, raw := u.do("GET", "/api/memory", nil); !strings.Contains(string(raw), "prefers spaces") {
		t.Fatalf("%s", raw)
	}
	if code, _, _ := u.do("DELETE", fmt.Sprintf("/api/memory/%d", id), nil); code != 200 {
		t.Fatal("delete")
	}
	if code, _, _ := u.do("DELETE", fmt.Sprintf("/api/memory/%d", id), nil); code != 404 {
		t.Fatal("deleted twice")
	}
}

func TestNotesAreLimitedPerUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < maxNotesPerUser; i++ {
		if _, err := e.app.addNote(ctx, 7, agent.MemoryUser, "", fmt.Sprintf("note %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.app.addNote(ctx, 7, agent.MemoryUser, "", "one too many"); err == nil {
		t.Fatal("no limit on notes")
	}
	if _, err := e.app.addNote(ctx, 8, agent.MemoryUser, "", "another user is not affected"); err != nil {
		t.Fatal(err)
	}
}

func TestNotesForASessionAreTheUsersOwnAndTheRepositorys(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	add := func(uid int64, scope, key, text string) int64 {
		id, err := e.app.addNote(ctx, uid, scope, key, text)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mine := add(1, agent.MemoryUser, "", "prefers tabs")
	repoA := add(1, agent.MemoryRepo, "acme/app", "run make check")
	add(1, agent.MemoryRepo, "acme/other", "other repo's note")
	add(2, agent.MemoryUser, "", "another user's note")

	got := e.app.notesFor(ctx, 1, "acme/app")
	if !strings.Contains(got, fmt.Sprintf("[%d] prefers tabs", mine)) || !strings.Contains(got, fmt.Sprintf("[%d] (this repository) run make check", repoA)) {
		t.Fatal(got)
	}
	if strings.Contains(got, "other repo") || strings.Contains(got, "another user") {
		t.Fatalf("notes from elsewhere: %s", got)
	}
	if got := e.app.notesFor(ctx, 1, ""); strings.Contains(got, "make check") {
		t.Fatalf("a session with no repository got a repository's notes: %s", got)
	}
	// newlines in a note cannot make a second "note"
	add(3, agent.MemoryUser, "", "line one\n[999] fake note")
	if got := e.app.notesFor(ctx, 3, ""); strings.Count(got, "\n") != 0 {
		t.Fatalf("%q", got)
	}
	// too many notes: the newest are kept
	for i := 0; i < 90; i++ {
		add(4, agent.MemoryUser, "", fmt.Sprintf("note number %02d %s", i, strings.Repeat("z", 100)))
	}
	got = e.app.notesFor(ctx, 4, "")
	if len(got) > maxNotesPrompt || !strings.Contains(got, "note number 89") || strings.Contains(got, "note number 00") {
		t.Fatalf("%d %q", len(got), got[:60])
	}
}

func TestAgentCanOnlyForgetNotesItCanSee(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mem := func(uid int64, repo string) sessionMemory {
		return sessionMemory{a: e.app, userID: uid, repo: repo, redact: func(s string) string { return strings.ReplaceAll(s, "SECRET", "[redacted]") }}
	}
	own, _ := mem(1, "acme/app").Add(ctx, agent.MemoryUser, "mine")
	repoNote, _ := mem(1, "acme/app").Add(ctx, agent.MemoryRepo, "about app")
	theirs, _ := mem(2, "").Add(ctx, agent.MemoryUser, "theirs")
	if err := mem(1, "acme/other").Forget(ctx, repoNote); err == nil {
		t.Fatal("a session for another repository deleted this repository's note")
	}
	if err := mem(1, "acme/app").Forget(ctx, theirs); err == nil {
		t.Fatal("deleted another user's note")
	}
	if err := mem(1, "acme/app").Forget(ctx, repoNote); err != nil {
		t.Fatal(err)
	}
	if err := mem(1, "").Forget(ctx, own); err != nil {
		t.Fatal(err)
	}
	if _, err := mem(1, "").Add(ctx, agent.MemoryRepo, "x"); err == nil {
		t.Fatal("a repository note without a repository")
	}
	id, _ := mem(5, "").Add(ctx, agent.MemoryUser, "token is SECRET")
	ns, _ := e.st.ListMemory(ctx, 5)
	if len(ns) != 1 || ns[0].ID != id || strings.Contains(ns[0].Content, "SECRET") {
		t.Fatalf("secrets are redacted before a note is stored: %+v", ns)
	}
}
