// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Editing and branching a conversation.
//
//   - Edit and resend (and regenerate, which is resending the same words): the messages from the
//     chosen one onward are set aside (they stay in the history and the export, marked as set aside)
//     and the agent runs again from there. Files the agent changed are not put back: use a
//     checkpoint for that, as the confirmation says.
//   - Branch: a new session that starts with a copy of the conversation up to a chosen reply. It uses
//     the same model, settings and workspace. In a session on a repository the new session gets its
//     own working branch, started where the old one is, so the two can go different ways.

// handleEdit sets the conversation aside from one of the user's messages and sends new text from there.
func (a *App) handleEdit(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	u := a.user(r)
	if run := a.Runs.Get(s.ID); run != nil && run.IsRunning() {
		writeErr(w, http.StatusConflict, "this session is already working; stop it first")
		return
	}
	ms, err := a.Store.ListMessages(r.Context(), s.ID, false)
	if err != nil {
		writeErr(w, 500, "could not load messages")
		return
	}
	idx := -1
	for i, m := range ms {
		if m.ID == b.MessageID {
			idx = i
		}
	}
	if idx < 0 || ms[idx].Role != "user" {
		writeErr(w, http.StatusBadRequest, "choose one of your own messages")
		return
	}
	text := b.Text
	// the files attached to the message stay attached to the new text
	_, items := agent.ParseAttachments(ms[idx].Content)
	if expanded, c := expandCommand(text, a.commandsFor(r.Context(), s, u.ID)); c != nil {
		text = expanded
	}
	text = agent.ComposeAttachments(text, items)
	if strings.TrimSpace(text) == "" || len(text) > 100_000 {
		writeErr(w, http.StatusBadRequest, "message is empty or too long")
		return
	}
	if full, used, limit := a.workspaceFull(r.Context(), u.ID, s.ID); full {
		writeErr(w, http.StatusInsufficientStorage, workspaceFullMessage(used, limit))
		return
	}
	var before int64 // everything after this id is set aside
	if idx > 0 {
		before = ms[idx-1].ID
	}
	n, err := a.Store.AbandonMessagesAfter(r.Context(), s.ID, before)
	if err != nil {
		writeErr(w, 500, "could not rewind the conversation")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), "user:"+itoa(u.ID), "session.edit", "session:"+s.ID, itoa(n)+" messages set aside")
	a.startRun(w, r, s, text)
}

// handleFork copies a conversation, up to a chosen reply, into a new session.
func (a *App) handleFork(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		MessageID int64 `json:"message_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	u := a.user(r)
	ms, err := a.Store.ListMessages(r.Context(), s.ID, false)
	if err != nil {
		writeErr(w, 500, "could not load messages")
		return
	}
	end := len(ms) // exclusive
	if b.MessageID != 0 {
		end = -1
		for i, m := range ms {
			if m.ID == b.MessageID {
				end = i + 1
			}
		}
		if end < 0 {
			writeErr(w, http.StatusBadRequest, "no such message")
			return
		}
	}
	if end == 0 {
		writeErr(w, http.StatusBadRequest, "there is nothing to branch from yet")
		return
	}
	// a copy must end between turns, never inside a tool call that has no result yet
	if last := ms[end-1]; last.Role == "tool" || (last.Role == "assistant" && last.ToolCalls != "") {
		writeErr(w, http.StatusBadRequest, "branch from one of the assistant's replies or one of your messages")
		return
	}
	existing, _ := a.Store.ListChatSessions(r.Context(), u.ID)
	active := 0
	for _, e := range existing {
		if e.State == "running" || e.State == "needs_input" {
			active++
		}
	}
	if err := a.Quota.CheckSandboxStart(r.Context(), u, active); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	now := a.Now().UTC()
	c := store.ChatSession{ID: newID(), UserID: u.ID, Title: branchTitle(s.Title), ProviderID: s.ProviderID, Model: s.Model, Autonomy: s.Autonomy,
		State: "idle", CreatedAt: now, UpdatedAt: now, Image: s.Image, EnvNote: s.EnvNote,
		RepoKind: s.RepoKind, RepoName: s.RepoName, RepoURL: s.RepoURL, RepoBase: s.RepoBase}
	if s.RepoName != "" {
		c.RepoBranch = workBranch(c.ID)
		if s.SandboxID != "" && a.Sandbox != nil {
			if err := a.branchRepo(r.Context(), s, c.RepoBranch); err != nil {
				writeErr(w, http.StatusBadGateway, "could not start a branch of the repository: "+err.Error())
				return
			}
		}
	}
	if err := a.Store.CreateChatSession(r.Context(), c); err != nil {
		writeErr(w, 500, "could not create the session")
		return
	}
	for _, m := range ms[:end] {
		m.ID, m.SessionID = 0, c.ID
		if _, err := a.Store.AppendMessage(r.Context(), m); err != nil {
			_ = a.Store.DeleteChatSession(r.Context(), c.ID)
			writeErr(w, 500, "could not copy the conversation")
			return
		}
	}
	_, _ = a.Store.AppendAudit(r.Context(), "user:"+itoa(u.ID), "session.fork", "session:"+s.ID, "new session "+c.ID)
	writeJSON(w, http.StatusCreated, sessionJSON(c, nil))
}

func branchTitle(t string) string {
	t = strings.TrimSpace(t)
	if len(t) > 80 {
		t = t[:80]
	}
	return t + " (branch)"
}

// branchRepo starts a working branch for a branched session where the old session's branch is.
func (a *App) branchRepo(ctx context.Context, s *store.ChatSession, newBranch string) error {
	dir := sandbox.WorkspacePath + "/" + sessionRepoDir(s)
	res, err := a.Sandbox.Exec(ctx, s.ID, s.SandboxID, sandbox.ExecSpec{
		Cmd:        []string{"sh", "-c", `cd "$1" && git branch -- "$3" "$2"`, "_", dir, s.RepoBranch, newBranch},
		WorkingDir: "/", Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &statusError{http.StatusBadGateway, strings.TrimSpace(res.Stderr)}
	}
	return nil
}
