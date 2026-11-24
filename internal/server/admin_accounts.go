// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Admin accounts are separate from chat users: they sign in to the admin app only, always with
// 2FA. This is where they are listed and kept: invite another admin, give one a fresh link when
// they are locked out, disable or remove one. Every change is audited. The caller is always an
// enabled admin and may not act on themself, so at least one admin who can sign in always remains.

const adminSetupTTL = 24 * time.Hour

type adminAccountJSON struct {
	ID          int64     `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	TOTPEnabled bool      `json:"totp_enabled"`
	Disabled    bool      `json:"disabled"`
	Pending     bool      `json:"pending"` // invited, but nobody has opened the setup link yet
	You         bool      `json:"you"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a *App) adminAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := a.Store.ListAccounts(r.Context(), store.KindAdmin)
	if err != nil {
		writeErr(w, 500, "could not load admin accounts")
		return
	}
	me := a.actorID(r)
	out := make([]adminAccountJSON, 0, len(accts))
	for _, x := range accts {
		out = append(out, adminAccountJSON{ID: x.ID, Email: x.Email, Name: x.Name, TOTPEnabled: x.TOTPEnabled, Disabled: x.Disabled,
			Pending: x.PasswordHash == "", You: x.ID == me, CreatedAt: x.CreatedAt})
	}
	writeJSON(w, 200, out)
}

// setupResponse carries the single-use link. It is shown to the admin who asked, once, and never
// emailed. The token is a URL fragment so it never reaches a server log or a Referer header.
func setupResponse(id int64, tok string) map[string]any {
	return map[string]any{"id": id, "setup_path": "/#setup=" + tok, "expires_hours": int(adminSetupTTL.Hours())}
}

func (a *App) adminAccountInvite(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Name == "" {
		writeErr(w, 400, "a name is needed")
		return
	}
	id, tok, err := a.Auth.InviteAdmin(r.Context(), a.actorID(r), b.Email, b.Name, adminSetupTTL)
	if err != nil {
		code := 400
		if errors.Is(err, store.ErrConflict) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	a.refreshHealth()
	writeJSON(w, http.StatusCreated, setupResponse(id, tok))
}

func (a *App) adminAccountReset(w http.ResponseWriter, r *http.Request) {
	tok, err := a.Auth.ResetAdmin(r.Context(), a.actorID(r), pathID(r), adminSetupTTL)
	if err != nil {
		code := 400
		switch {
		case errors.Is(err, auth.ErrForbidden):
			code = 403
		case errors.Is(err, store.ErrNotFound):
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	a.refreshHealth()
	writeJSON(w, 200, setupResponse(pathID(r), tok))
}

func (a *App) adminAccountPatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Disabled *bool `json:"disabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Disabled == nil {
		writeErr(w, 400, "nothing to change")
		return
	}
	id := pathID(r)
	if *b.Disabled && id == a.actorID(r) {
		writeErr(w, 403, "you cannot disable your own account")
		return
	}
	acct, err := a.Store.GetAccount(r.Context(), store.KindAdmin, id)
	if err != nil {
		writeErr(w, 404, "no such admin")
		return
	}
	acct.Disabled = *b.Disabled
	if err := a.Store.UpdateAccount(r.Context(), *acct); err != nil {
		writeErr(w, 500, "could not save")
		return
	}
	if acct.Disabled {
		_ = a.Store.DeleteAuthSessionsFor(r.Context(), store.KindAdmin, id)
	}
	action := "admin.enable"
	if acct.Disabled {
		action = "admin.disable"
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), action, fmt.Sprintf("admin:%d", id), acct.Email)
	a.refreshHealth()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if id == a.actorID(r) {
		writeErr(w, 403, "you cannot remove your own account")
		return
	}
	acct, err := a.Store.GetAccount(r.Context(), store.KindAdmin, id)
	if err != nil {
		writeErr(w, 404, "no such admin")
		return
	}
	if err := a.Store.DeleteAccount(r.Context(), store.KindAdmin, id); err != nil {
		writeErr(w, 500, "could not remove")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "admin.remove", fmt.Sprintf("admin:%d", id), acct.Email)
	a.refreshHealth()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
