// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/policy"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Levels (user, poweruser, admin) and groups carry the same kind of settings; see package
// policy for how they combine with a user's own.

func policyJSON(p store.Policy) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "policy": p.Data}
}

func (a *App) adminLevels(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, name := range []string{store.LevelUser, store.LevelPowerUser, store.LevelAdmin} {
		p := store.Policy{Kind: "level", Name: name}
		if got, err := a.Store.GetPolicy(r.Context(), "level", name); err == nil {
			p = *got
		}
		out = append(out, policyJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) adminLevelPut(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name != store.LevelUser && name != store.LevelPowerUser && name != store.LevelAdmin {
		writeErr(w, http.StatusNotFound, "no such level")
		return
	}
	var d store.PolicyData
	if !decode(w, r, &d) {
		return
	}
	if msg := policy.Valid(d); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if _, err := a.Store.UpsertPolicy(r.Context(), store.Policy{Kind: "level", Name: name, Data: d}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "policy.level", "level:"+name, "changed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := a.Store.ListPolicies(r.Context(), "group")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list groups")
		return
	}
	users, _ := a.Store.ListUsers(r.Context())
	members := map[int64]int{}
	for _, u := range users {
		members[u.GroupID]++
	}
	out := []map[string]any{}
	for _, g := range gs {
		m := policyJSON(g)
		m["members"] = members[g.ID]
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) adminGroupSave(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name   string           `json:"name"`
		Policy store.PolicyData `json:"policy"`
	}
	if !decode(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 60 {
		writeErr(w, http.StatusBadRequest, "a group needs a name (up to 60 characters)")
		return
	}
	if msg := policy.Valid(b.Policy); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	p := store.Policy{Kind: "group", Name: b.Name, Data: b.Policy}
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		p.ID = pathID(r)
		if got, err := a.Store.GetPolicyByID(r.Context(), p.ID); err != nil || got.Kind != "group" {
			writeErr(w, http.StatusNotFound, "no such group")
			return
		}
	} else if _, err := a.Store.GetPolicy(r.Context(), "group", b.Name); err == nil {
		writeErr(w, http.StatusConflict, "a group with that name already exists")
		return
	}
	id, err := a.Store.UpsertPolicy(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusConflict, "could not save (is the name already used?)")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "policy.group", "group:"+itoa(id), b.Name)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (a *App) adminGroupDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if got, err := a.Store.GetPolicyByID(r.Context(), id); err != nil || got.Kind != "group" {
		writeErr(w, http.StatusNotFound, "no such group")
		return
	}
	if err := a.Store.DeletePolicy(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "policy.group.delete", "group:"+itoa(id), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
