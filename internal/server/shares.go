// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/store"
)

// A share is a disposable, read-only link to one session, for a number of hours the owner chooses.
// Anyone with the link can read the conversation (and, if the owner allowed it, the command output)
// until it expires or the owner revokes it; they cannot do anything in the session. The secret in
// the link is stored only as a hash for lookups and sealed with the master key so the owner can copy
// the link again.

const (
	maxShareHours       = 24 * 30
	maxActiveSharesUser = 50
)

func shareAAD(id string) string { return "share:" + id }

func (a *App) shareURL(token string) string {
	return strings.TrimRight(a.Cfg.BaseURL, "/") + "/s/" + token
}

func (a *App) shareJSON(sh store.Share, withURL bool) map[string]any {
	m := map[string]any{
		"id": sh.ID, "session_id": sh.SessionID, "name": sh.Name, "include_tools": sh.IncludeTools,
		"created_at": sh.CreatedAt, "expires_at": sh.ExpiresAt, "revoked": sh.Revoked, "views": sh.Views, "last_view_at": sh.LastViewAt,
		"active": !sh.Revoked && a.Now().Before(sh.ExpiresAt),
	}
	if withURL && !sh.Revoked {
		if pt, err := a.Secrets.Vault.Open(sh.TokenEnc, shareAAD(sh.ID)); err == nil {
			m["url"] = a.shareURL(string(pt))
		}
	}
	return m
}

func (a *App) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Name         string `json:"name"`
		Hours        int    `json:"hours"`
		IncludeTools bool   `json:"include_tools"`
	}
	if !decode(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		b.Name = s.Title
	}
	if len(b.Name) > 80 {
		writeErr(w, http.StatusBadRequest, "the name is too long")
		return
	}
	if b.Hours < 1 || b.Hours > maxShareHours {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("choose between 1 and %d hours", maxShareHours))
		return
	}
	u := a.user(r)
	if n, _ := a.Store.CountActiveShares(r.Context(), u.ID, a.Now()); n >= maxActiveSharesUser {
		writeErr(w, http.StatusConflict, fmt.Sprintf("you have %d active links; revoke some first", maxActiveSharesUser))
		return
	}
	tok, hash := newHookToken()
	id := newID()
	enc, err := a.Secrets.Vault.Seal([]byte(tok), shareAAD(id))
	if err != nil {
		writeErr(w, 500, "could not create the link")
		return
	}
	now := a.Now().UTC()
	sh := store.Share{ID: id, SessionID: s.ID, UserID: u.ID, Name: b.Name, TokenHash: hash, TokenEnc: enc, IncludeTools: b.IncludeTools, CreatedAt: now, ExpiresAt: now.Add(time.Duration(b.Hours) * time.Hour)}
	if err := a.Store.CreateShare(r.Context(), sh); err != nil {
		writeErr(w, 500, "could not create the link")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", u.ID), "share.create", "session:"+s.ID, fmt.Sprintf("%dh", b.Hours))
	writeJSON(w, http.StatusCreated, a.shareJSON(sh, true))
}

func (a *App) handleShares(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	list, _ := a.Store.ListShares(r.Context(), u.ID)
	titles := map[string]string{}
	out := []map[string]any{}
	for _, sh := range list {
		m := a.shareJSON(sh, true)
		if _, ok := titles[sh.SessionID]; !ok {
			if s, err := a.Store.GetChatSession(r.Context(), sh.SessionID); err == nil {
				titles[sh.SessionID] = s.Title
			} else {
				titles[sh.SessionID] = "(deleted session)"
			}
		}
		m["session_title"] = titles[sh.SessionID]
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	sh, err := a.Store.GetShare(r.Context(), chi.URLParam(r, "id"))
	if err != nil || sh.UserID != a.user(r).ID {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	_ = a.Store.RevokeShare(r.Context(), sh.ID)
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", sh.UserID), "share.revoke", "session:"+sh.SessionID, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleShared is the public, read-only view. An unknown, expired and revoked link all look the same.
func (a *App) handleShared(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.Cfg.ProxyAuth.Trusted)
	if hooks.tooMany(ip, a.Now()) {
		writeErr(w, http.StatusTooManyRequests, "too many wrong links")
		return
	}
	sh, err := a.Store.GetShareByHash(r.Context(), hashToken(chi.URLParam(r, "token")))
	if err != nil || sh.Revoked || !a.Now().Before(sh.ExpiresAt) {
		hooks.note(ip, a.Now())
		writeErr(w, http.StatusNotFound, "this link does not exist or has expired")
		return
	}
	s, err := a.Store.GetChatSession(r.Context(), sh.SessionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "this link does not exist or has expired")
		return
	}
	_ = a.Store.RecordShareView(r.Context(), sh.ID, a.Now())
	_, red, _ := a.Secrets.Snapshot(r.Context(), s.UserID)
	redact := func(t string) string {
		if red == nil {
			return t
		}
		return red.String(t)
	}
	ms, _ := a.Store.ListMessages(r.Context(), s.ID, false)
	out := []map[string]any{}
	for _, m := range ms {
		switch m.Role {
		case "user", "assistant":
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, map[string]any{"role": m.Role, "content": redact(m.Content), "created_at": m.CreatedAt})
			}
		case "tool":
			if sh.IncludeTools {
				out = append(out, map[string]any{"role": "tool", "content": redact(m.Content), "created_at": m.CreatedAt})
			}
		}
	}
	state := s.State
	if run := a.Runs.Get(s.ID); run != nil && run.IsRunning() {
		state = "running"
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, map[string]any{"title": s.Title, "model": s.Model, "state": state, "expires_at": sh.ExpiresAt, "include_tools": sh.IncludeTools, "messages": out})
}
