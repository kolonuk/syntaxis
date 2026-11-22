// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Personal API tokens let a script or a tool act as one user with a limited scope. A token is made
// in Settings (with a signed-in session; a token can never make or change tokens), shown once, and
// stored only as a hash. It works on the chat app's /api and nowhere else (never the admin app).
//
// Scopes:
//   - read: look at the user's sessions, messages, events, changes and files, and the model list.
//   - run:  read, plus start a session, send a message, stop it, answer its questions and
//     approvals, approve a plan, and branch it.
//
// Everything not listed below is refused for a token, including settings, secrets, connections,
// hooks, automations, shares, the terminal, deleting or editing files, and 2FA or password changes.
const (
	TokenPrefix = "syx_"
	ScopeRead   = "read"
	ScopeRun    = "run"

	maxAPITokens = 20
)

type tokenRule struct {
	method string
	re     *regexp.Regexp
	scope  string // the least scope that may do it
}

func rule(method, pattern, scope string) tokenRule {
	return tokenRule{method, regexp.MustCompile("^/api" + pattern + "$"), scope}
}

var seg = `[A-Za-z0-9_-]+`

// tokenRules is the whole of what a token may do; anything else is refused.
var tokenRules = []tokenRule{
	rule("GET", `/me`, ScopeRead),
	rule("GET", `/models`, ScopeRead),
	rule("GET", `/usage`, ScopeRead),
	rule("GET", `/environments`, ScopeRead),
	rule("GET", `/sessions`, ScopeRead),
	rule("GET", `/sessions/`+seg, ScopeRead),
	rule("GET", `/sessions/`+seg+`/messages`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/events`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/diff`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/checkpoints`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/checkpoints/[0-9]+/diff`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/export`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/resources`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/files`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/files/content`, ScopeRead),
	rule("GET", `/sessions/`+seg+`/files/download`, ScopeRead),
	rule("POST", `/sessions`, ScopeRun),
	rule("POST", `/sessions/`+seg+`/messages`, ScopeRun),
	rule("POST", `/sessions/`+seg+`/stop`, ScopeRun),
	rule("POST", `/sessions/`+seg+`/approvals/`+seg, ScopeRun),
	rule("POST", `/sessions/`+seg+`/answer`, ScopeRun),
	rule("POST", `/sessions/`+seg+`/plan`, ScopeRun),
	rule("POST", `/sessions/`+seg+`/fork`, ScopeRun),
}

func scopeRank(s string) int {
	switch s {
	case ScopeRead:
		return 1
	case ScopeRun:
		return 2
	}
	return 0
}

// tokenAllowed says whether a token of this scope may make this request.
func tokenAllowed(scope, method, urlPath string) bool {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	for _, r := range tokenRules {
		if r.method == method && r.re.MatchString(urlPath) {
			return scopeRank(scope) >= scopeRank(r.scope)
		}
	}
	return false
}

type ctxTokenKey struct{}

func tokenFrom(r *http.Request) *store.APIToken {
	t, _ := r.Context().Value(ctxTokenKey{}).(*store.APIToken)
	return t
}

// bearerToken returns the API token in the request, if it has one.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authenticateToken checks a bearer token and returns the user it acts for.
func (a *App) authenticateToken(ctx context.Context, raw string) (*auth.Principal, *store.APIToken, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return nil, nil, auth.ErrNoSession
	}
	t, err := a.Store.GetAPITokenByHash(ctx, auth.HashToken(raw))
	if err != nil || !t.RevokedAt.IsZero() || (!t.ExpiresAt.IsZero() && !a.Now().Before(t.ExpiresAt)) {
		return nil, nil, auth.ErrNoSession
	}
	acct, err := a.Store.GetAccount(ctx, store.KindUser, t.UserID)
	if err != nil || acct.Disabled {
		return nil, nil, auth.ErrNoSession
	}
	if a.Now().Sub(t.LastUsedAt) > time.Minute {
		_ = a.Store.TouchAPIToken(ctx, t.ID, a.Now())
	}
	// no cookie session behind a token: no CSRF token, and nothing that needs a session can run
	return &auth.Principal{Account: acct, Session: &store.AuthSession{Kind: store.KindUser, OwnerID: acct.ID}}, t, nil
}

func tokenView(t store.APIToken) map[string]any {
	m := map[string]any{"id": t.ID, "name": t.Name, "prefix": t.Prefix, "scope": t.Scope, "created_at": t.CreatedAt, "revoked": !t.RevokedAt.IsZero()}
	if !t.ExpiresAt.IsZero() {
		m["expires_at"] = t.ExpiresAt
	}
	if !t.LastUsedAt.IsZero() {
		m["last_used_at"] = t.LastUsedAt
	}
	return m
}

func (a *App) handleTokensList(w http.ResponseWriter, r *http.Request) {
	ts, err := a.Store.ListAPITokens(r.Context(), principal(r).Account.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the tokens")
		return
	}
	out := []map[string]any{}
	for _, t := range ts {
		out = append(out, tokenView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	uid := principal(r).Account.ID
	var b struct {
		Name        string `json:"name"`
		Scope       string `json:"scope"`
		ExpiresDays int    `json:"expires_days"`
	}
	if !decode(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	switch {
	case b.Name == "" || len(b.Name) > 60:
		writeErr(w, http.StatusBadRequest, "give the token a name of up to 60 characters")
		return
	case b.Scope != ScopeRead && b.Scope != ScopeRun:
		writeErr(w, http.StatusBadRequest, "the scope is read or run")
		return
	case b.ExpiresDays < 0 || b.ExpiresDays > 3650:
		writeErr(w, http.StatusBadRequest, "expires_days is 0 (never) to 3650")
		return
	}
	existing, _ := a.Store.ListAPITokens(r.Context(), uid)
	active := 0
	for _, t := range existing {
		if t.RevokedAt.IsZero() {
			active++
		}
	}
	if active >= maxAPITokens {
		writeErr(w, http.StatusConflict, fmt.Sprintf("you can have %d tokens; revoke one first", maxAPITokens))
		return
	}
	raw, hash, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not make a token")
		return
	}
	raw = TokenPrefix + raw
	hash = auth.HashToken(raw)
	t := store.APIToken{UserID: uid, Name: b.Name, Prefix: raw[:len(TokenPrefix)+6], Scope: b.Scope, CreatedAt: a.Now()}
	if b.ExpiresDays > 0 {
		t.ExpiresAt = a.Now().Add(time.Duration(b.ExpiresDays) * 24 * time.Hour)
	}
	id, err := a.Store.CreateAPIToken(r.Context(), t, hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the token")
		return
	}
	t.ID = id
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", uid), "token.create", fmt.Sprintf("token:%d", id), fmt.Sprintf("%s scope=%s", t.Name, t.Scope))
	v := tokenView(t)
	v["token"] = raw // shown this once
	writeJSON(w, http.StatusCreated, v)
}

func (a *App) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	uid := principal(r).Account.ID
	id := pathIDParam(r, "id")
	if err := a.Store.RevokeAPIToken(r.Context(), uid, id, a.Now()); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", uid), "token.revoke", fmt.Sprintf("token:%d", id), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func pathIDParam(r *http.Request, name string) int64 {
	var n int64
	_, _ = fmt.Sscan(chi.URLParam(r, name), &n)
	return n
}
