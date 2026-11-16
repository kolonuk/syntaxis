// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/store"
)

// A user's connections are their own: the credentials are secrets in that user's vault (secret
// fields) or settings on their connection record (the rest), and every endpoint here works only
// on the signed-in user's. Credentials are never returned.

// credsFor reads the user's stored values for a service.
func (a *App) credsFor(ctx context.Context, userID int64, k connections.Kind) connections.Creds {
	cs, _ := a.Store.ListConnections(ctx, userID)
	settings := map[string]string{}
	for _, c := range cs {
		if c.Kind == k.ID {
			settings = c.Settings
		}
	}
	out := connections.Creds{}
	for _, f := range k.Fields {
		if f.Secret {
			if v, err := a.Secrets.Get(ctx, secrets.ScopeUser, userID, f.Name); err == nil {
				out[f.Name] = v
			}
		} else if v, ok := settings[f.Name]; ok {
			out[f.Name] = v
		}
	}
	return out
}

func missingRequired(k connections.Kind, c connections.Creds) bool {
	for _, f := range k.Fields {
		if f.Required && strings.TrimSpace(c[f.Name]) == "" {
			return true
		}
	}
	return false
}

func (a *App) handleConnections(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	have := map[string]store.Connection{}
	cs, _ := a.Store.ListConnections(r.Context(), u.ID)
	for _, c := range cs {
		have[c.Kind] = c
	}
	out := []map[string]any{}
	for _, k := range a.Connections.Kinds() {
		creds := a.credsFor(r.Context(), u.ID, k)
		rec := have[k.ID]
		values := map[string]string{} // only the non-secret fields
		stored := map[string]bool{}   // which secret fields are set (never their values)
		for _, f := range k.Fields {
			if f.Secret {
				stored[f.Name] = creds[f.Name] != ""
			} else {
				values[f.Name] = creds[f.Name]
			}
		}
		out = append(out, map[string]any{
			"kind": k, "connected": !missingRequired(k, creds), "account": rec.Account, "detail": rec.Detail,
			"checked_at": rec.CheckedAt, "values": values, "stored": stored,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) provider(w http.ResponseWriter, r *http.Request) (connections.Provider, bool) {
	p, ok := a.Connections.Get(chi.URLParam(r, "kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such service")
	}
	return p, ok
}

// saveConnection stores a tested set of values: secret fields in the vault, the rest on the record.
func (a *App) saveConnection(ctx context.Context, userID int64, k connections.Kind, creds connections.Creds, id connections.Identity) error {
	settings := map[string]string{}
	for _, f := range k.Fields {
		v := creds[f.Name]
		switch {
		case f.Secret && v != "":
			if err := a.Secrets.Put(ctx, secrets.ScopeUser, userID, f.Name, v); err != nil {
				return err
			}
		case f.Secret:
			_ = a.Secrets.Delete(ctx, secrets.ScopeUser, userID, f.Name) // an optional secret that was cleared
		case v != "":
			settings[f.Name] = v
		}
	}
	return a.Store.PutConnection(ctx, store.Connection{UserID: userID, Kind: k.ID, Account: id.Account, Detail: id.Detail, Settings: settings, CheckedAt: a.Now()})
}

func (a *App) handleConnectionPut(w http.ResponseWriter, r *http.Request) {
	p, ok := a.provider(w, r)
	if !ok {
		return
	}
	u := a.user(r)
	k := p.Kind()
	var b struct {
		Fields map[string]string `json:"fields"`
	}
	if !decode(w, r, &b) {
		return
	}
	// start from what is stored, so a single field (a new address, say) can be changed without
	// typing the token again; a secret sent empty keeps its stored value
	creds := a.credsFor(r.Context(), u.ID, k)
	known := map[string]connections.Field{}
	for _, f := range k.Fields {
		known[f.Name] = f
	}
	for name, v := range b.Fields {
		f, ok := known[name]
		if !ok {
			writeErr(w, http.StatusBadRequest, "unknown field "+name)
			return
		}
		if f.Secret && v == "" {
			continue
		}
		creds[name] = strings.TrimSpace(v)
	}
	for _, f := range k.Fields {
		if creds[f.Name] == "" && f.Default != "" && !f.Secret {
			creds[f.Name] = f.Default
		}
	}
	if err := connections.Validate(k, creds); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id, err := p.Test(ctx, creds)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "These credentials did not work: "+err.Error())
		return
	}
	if err := a.saveConnection(r.Context(), u.ID, k, creds, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the connection")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", u.ID), "connection.save", "connection:"+k.ID, id.Account)
	writeJSON(w, http.StatusOK, map[string]any{"account": id.Account, "detail": id.Detail})
}

func (a *App) handleConnectionDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.provider(w, r)
	if !ok {
		return
	}
	u := a.user(r)
	k := p.Kind()
	for _, f := range k.Fields {
		if f.Secret {
			_ = a.Secrets.Delete(r.Context(), secrets.ScopeUser, u.ID, f.Name)
		}
	}
	_ = a.Store.DeleteConnection(r.Context(), u.ID, k.ID)
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", u.ID), "connection.remove", "connection:"+k.ID, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	p, ok := a.provider(w, r)
	if !ok {
		return
	}
	u := a.user(r)
	k := p.Kind()
	creds := a.credsFor(r.Context(), u.ID, k)
	if missingRequired(k, creds) {
		writeErr(w, http.StatusBadRequest, "this service is not connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id, err := p.Test(ctx, creds)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = a.saveConnection(r.Context(), u.ID, k, creds, id)
	writeJSON(w, http.StatusOK, map[string]any{"account": id.Account, "detail": id.Detail})
}

func (a *App) gitProvider(w http.ResponseWriter, r *http.Request) (connections.GitProvider, connections.Creds, bool) {
	g, ok := a.Connections.Git(chi.URLParam(r, "kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, "this service has no repositories")
		return nil, nil, false
	}
	creds := a.credsFor(r.Context(), a.user(r).ID, g.Kind())
	if missingRequired(g.Kind(), creds) {
		writeErr(w, http.StatusBadRequest, "connect this service first")
		return nil, nil, false
	}
	return g, creds, true
}

func (a *App) handleConnectionRepos(w http.ResponseWriter, r *http.Request) {
	g, creds, ok := a.gitProvider(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rs, err := g.ListRepos(ctx, creds, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rs == nil {
		rs = []connections.Repo{}
	}
	writeJSON(w, http.StatusOK, rs)
}

func (a *App) handleConnectionRepoCreate(w http.ResponseWriter, r *http.Request) {
	g, creds, ok := a.gitProvider(w, r)
	if !ok {
		return
	}
	var n connections.NewRepo
	if !decode(w, r, &n) {
		return
	}
	n.Name = strings.TrimSpace(n.Name)
	if !validRepoName(n.Name) {
		writeErr(w, http.StatusBadRequest, "a repository name uses letters, digits, dashes, dots and underscores")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	repo, err := g.CreateRepo(ctx, creds, n)
	if err != nil {
		if errors.Is(err, connections.ErrBadCredentials) {
			writeErr(w, http.StatusBadGateway, "the service refused; the token may not be allowed to create repositories ("+err.Error()+")")
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", a.user(r).ID), "connection.repo.create", "connection:"+g.Kind().ID, repo.FullName)
	writeJSON(w, http.StatusOK, repo)
}

func validRepoName(s string) bool {
	if s == "" || len(s) > 100 || strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
