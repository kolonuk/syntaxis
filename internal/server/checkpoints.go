// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/checkpoint"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// markCheckpoint remembers where in the conversation a checkpoint was taken.
func (a *App) markCheckpoint(ctx context.Context, userID int64, sessionID string, seq int, label string) {
	var last int64
	if ms, err := a.Store.ListMessages(ctx, sessionID, false); err == nil && len(ms) > 0 {
		last = ms[len(ms)-1].ID
	}
	_ = a.Store.PutCheckpointMark(ctx, store.CheckpointMark{UserID: userID, Seq: seq, SessionID: sessionID, MessageID: last, Label: label, CreatedAt: a.Now()})
}

// checkpointsFor builds a checkpoint manager that runs git inside the session's sandbox.
func (a *App) checkpointsFor(ctx context.Context, s *store.ChatSession, u *store.User) (*checkpoint.Manager, error) {
	if a.Sandbox == nil || s.SandboxID == "" {
		return nil, fmt.Errorf("nothing has run in this session yet, so there are no checkpoints")
	}
	cid, err := a.ensureSandbox(ctx, s, u)
	if err != nil {
		return nil, err
	}
	runner := func(c context.Context, argv []string, env []string) (string, error) {
		r, err := a.Sandbox.Exec(c, s.ID, cid, sandbox.ExecSpec{Cmd: argv, Env: env, WorkingDir: "/", Timeout: 2 * time.Minute, MaxOutput: 2 << 20})
		if err != nil {
			return "", err
		}
		if r.ExitCode != 0 {
			return r.Stdout, fmt.Errorf("exit %d: %s", r.ExitCode, r.Stderr)
		}
		return r.Stdout, nil
	}
	return &checkpoint.Manager{Run: runner, Workspace: sandbox.WorkspacePath, MaxCount: 50, MaxBytes: 1 << 30}, nil
}

// handleCheckpoints lists the workspace's checkpoints, newest first, saying which belong to this
// session (a user's sessions share one workspace).
func (a *App) handleCheckpoints(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	u := a.user(r)
	m, err := a.checkpointsFor(r.Context(), s, u)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": err.Error()})
		return
	}
	cps, err := m.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not read the checkpoints: "+err.Error())
		return
	}
	marks, _ := a.Store.ListCheckpointMarks(r.Context(), u.ID)
	bySeq := map[int]store.CheckpointMark{}
	for _, mk := range marks {
		bySeq[mk.Seq] = mk
	}
	out := []map[string]any{}
	for i := len(cps) - 1; i >= 0; i-- {
		c := cps[i]
		mk, has := bySeq[c.Seq]
		out = append(out, map[string]any{"seq": c.Seq, "label": c.Label, "time": c.Time, "this_session": has && mk.SessionID == s.ID, "can_rewind_conversation": has && mk.SessionID == s.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "checkpoints": out})
}

func seqParam(r *http.Request) (int, bool) {
	n, err := strconv.Atoi(chi.URLParam(r, "seq"))
	return n, err == nil && n > 0
}

// handleCheckpointDiff shows what has changed in the workspace since a checkpoint.
func (a *App) handleCheckpointDiff(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	seq, ok := seqParam(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad checkpoint")
		return
	}
	m, err := a.checkpointsFor(r.Context(), s, a.user(r))
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	d, err := m.Diff(r.Context(), seq)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such checkpoint")
		return
	}
	if len(d) > 400_000 {
		d = d[:400_000]
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": a.redactFor(r.Context(), s.UserID, d)})
}

// handleCheckpointRestore puts the workspace back to a checkpoint and, if asked, rewinds the
// conversation to the same moment (the later messages are kept but set aside). A restore is itself
// checkpointed first, so it can be undone. Pushes and deploys are not undone.
func (a *App) handleCheckpointRestore(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	seq, ok := seqParam(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad checkpoint")
		return
	}
	var b struct {
		Conversation bool `json:"conversation"`
	}
	if !decode(w, r, &b) {
		return
	}
	if run := a.Runs.Get(s.ID); run != nil && run.IsRunning() {
		writeErr(w, http.StatusConflict, "the agent is working; stop it first")
		return
	}
	u := a.user(r)
	if b.Conversation {
		if mk, err := a.Store.GetCheckpointMark(r.Context(), u.ID, seq); err != nil || mk.SessionID != s.ID {
			writeErr(w, http.StatusBadRequest, "this checkpoint was taken in another session, so the conversation cannot be rewound to it")
			return
		}
	}
	m, err := a.checkpointsFor(r.Context(), s, u)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := m.Restore(r.Context(), seq); err != nil {
		writeErr(w, http.StatusBadGateway, "could not restore: "+err.Error())
		return
	}
	// the safety snapshot taken before the restore belongs to this session's conversation as it is now
	if cps, err := m.List(r.Context()); err == nil && len(cps) > 0 {
		a.markCheckpoint(r.Context(), u.ID, s.ID, cps[len(cps)-1].Seq, cps[len(cps)-1].Label)
	}
	rewound := int64(0)
	if b.Conversation {
		mk, _ := a.Store.GetCheckpointMark(r.Context(), u.ID, seq)
		rewound, _ = a.Store.AbandonMessagesAfter(r.Context(), s.ID, mk.MessageID)
	}
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", u.ID), "checkpoint.restore", "session:"+s.ID, fmt.Sprintf("seq=%d conversation=%v", seq, b.Conversation))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages_set_aside": rewound})
}
