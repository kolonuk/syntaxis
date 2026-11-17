// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Notes the agent keeps across sessions. They are per user, and per repository for notes about one.
// The agent can add and delete them (the user approves each change); the user can list, add, edit
// and delete them in Settings.
const (
	maxNotesPerUser = 100
	maxNotesPrompt  = 4000 // characters of notes put in a session's instructions
)

// notesFor returns the notes that apply to a session (the user's own, and the repository's), one per
// line as "[id] text", newest last, cut to what fits the instructions.
func (a *App) notesFor(ctx context.Context, userID int64, repo string) string {
	ns, err := a.Store.ListMemory(ctx, userID)
	if err != nil {
		return ""
	}
	var lines []string
	for _, n := range ns {
		switch {
		case n.Scope == agent.MemoryUser:
			lines = append(lines, fmt.Sprintf("[%d] %s", n.ID, oneLine(n.Content)))
		case n.Scope == agent.MemoryRepo && repo != "" && n.ScopeKey == repo:
			lines = append(lines, fmt.Sprintf("[%d] (this repository) %s", n.ID, oneLine(n.Content)))
		}
	}
	// when there are too many, the newest are kept
	total := 0
	start := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if total += len(lines[i]) + 1; total > maxNotesPrompt {
			break
		}
		start = i
	}
	return strings.Join(lines[start:], "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// sessionMemory is the agent's view of the notes of one session.
type sessionMemory struct {
	a      *App
	userID int64
	repo   string
	redact func(string) string
}

func (m sessionMemory) Add(ctx context.Context, scope, text string) (int64, error) {
	if scope == agent.MemoryRepo && m.repo == "" {
		return 0, errors.New("this session has no repository; save the note with scope user")
	}
	key := ""
	if scope == agent.MemoryRepo {
		key = m.repo
	}
	return m.a.addNote(ctx, m.userID, scope, key, m.redact(text))
}

func (m sessionMemory) Forget(ctx context.Context, id int64) error {
	ns, err := m.a.Store.ListMemory(ctx, m.userID)
	if err != nil {
		return err
	}
	for _, n := range ns {
		// only notes that are shown to this session can be deleted by it
		if n.ID == id && (n.Scope == agent.MemoryUser || n.ScopeKey == m.repo) {
			return m.a.Store.DeleteMemory(ctx, m.userID, id)
		}
	}
	return errors.New("there is no such note")
}

func (a *App) addNote(ctx context.Context, userID int64, scope, key, text string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > agent.MaxNoteLength {
		return 0, fmt.Errorf("a note needs 1 to %d characters", agent.MaxNoteLength)
	}
	ns, err := a.Store.ListMemory(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(ns) >= maxNotesPerUser {
		return 0, fmt.Errorf("there are already %d notes; delete some first", maxNotesPerUser)
	}
	now := a.Now().UTC()
	return a.Store.AddMemory(ctx, store.MemoryNote{UserID: userID, Scope: scope, ScopeKey: key, Content: text, CreatedAt: now, UpdatedAt: now})
}

func noteJSON(n store.MemoryNote) map[string]any {
	return map[string]any{"id": n.ID, "scope": n.Scope, "repo": n.ScopeKey, "content": n.Content, "updated_at": n.UpdatedAt}
}

func (a *App) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	ns, err := a.Store.ListMemory(r.Context(), principal(r).Account.ID)
	if err != nil {
		writeErr(w, 500, "could not list notes")
		return
	}
	out := []map[string]any{}
	for _, n := range ns {
		out = append(out, noteJSON(n))
	}
	writeJSON(w, 200, out)
}

func (a *App) handleMemoryAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scope   string `json:"scope"`
		Repo    string `json:"repo"`
		Content string `json:"content"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Scope != agent.MemoryUser && b.Scope != agent.MemoryRepo {
		writeErr(w, 400, `scope must be "user" or "repo"`)
		return
	}
	if b.Scope == agent.MemoryRepo && !strings.Contains(strings.TrimSpace(b.Repo), "/") {
		writeErr(w, 400, "name the repository as owner/name")
		return
	}
	key := ""
	if b.Scope == agent.MemoryRepo {
		key = strings.TrimSpace(b.Repo)
	}
	id, err := a.addNote(r.Context(), principal(r).Account.ID, b.Scope, key, b.Content)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (a *App) handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b struct {
		Content string `json:"content"`
	}
	if err != nil || !decode(w, r, &b) {
		if err != nil {
			writeErr(w, 400, "bad note")
		}
		return
	}
	b.Content = strings.TrimSpace(b.Content)
	if b.Content == "" || len(b.Content) > agent.MaxNoteLength {
		writeErr(w, 400, fmt.Sprintf("a note needs 1 to %d characters", agent.MaxNoteLength))
		return
	}
	if err := a.Store.UpdateMemory(r.Context(), principal(r).Account.ID, id, b.Content, a.Now().UTC()); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad note")
		return
	}
	if err := a.Store.DeleteMemory(r.Context(), principal(r).Account.ID, id); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
