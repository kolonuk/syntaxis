// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/store"
)

// The admin runs the machine, so the admin can see and change what each user has set up: notes,
// slash commands, hooks, which connections exist (their names, never the credentials), MCP choices,
// automations and API tokens. Every change is written to the audit log. This is about settings; the
// admin can also read messages. What an admin still never gets is a user's stored credential values:
// they are encrypted with the master key and are not shown anywhere.

func (a *App) adminTargetUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, err := a.Store.GetUser(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such user")
		return nil, false
	}
	return u, true
}

func (a *App) adminAuditUser(r *http.Request, u *store.User, action, detail string) {
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), action, "user:"+itoa(u.ID), detail)
}

// adminUserSettings shows everything a user has configured.
func (a *App) adminUserSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	notes, _ := a.Store.ListMemory(ctx, u.ID)
	noteOut := []map[string]any{}
	for _, n := range notes {
		noteOut = append(noteOut, map[string]any{"id": n.ID, "scope": n.Scope, "scope_key": n.ScopeKey, "content": n.Content})
	}
	conns, _ := a.Store.ListConnections(ctx, u.ID)
	connOut := []map[string]any{}
	for _, c := range conns {
		connOut = append(connOut, map[string]any{"kind": c.Kind, "account": c.Account, "detail": c.Detail})
	}
	autos, _ := a.Store.ListAutomations(ctx, u.ID)
	autoOut := []map[string]any{}
	for _, au := range autos {
		autoOut = append(autoOut, map[string]any{"id": au.ID, "name": au.Name, "enabled": au.Enabled, "trigger": au.Trigger, "model": au.Model, "autonomy": au.Autonomy, "last_status": au.LastStatus})
	}
	toks, _ := a.Store.ListAPITokens(ctx, u.ID)
	tokOut := []map[string]any{}
	for _, t := range toks {
		tokOut = append(tokOut, tokenView(t))
	}
	var mcpOn []string
	for n := range a.userMCP(ctx, u.ID) {
		mcpOn = append(mcpOn, n)
	}
	sort.Strings(mcpOn)
	secretNames, _ := a.Secrets.Names(ctx, "user", u.ID)
	var trusted []string
	_ = json.Unmarshal([]byte(a.setting(ctx, hooksTrustKey(u.ID), "[]")), &trusted)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":               map[string]any{"id": u.ID, "email": u.Email, "name": u.Name},
		"notes":              noteOut,
		"commands":           a.userCommands(ctx, u.ID),
		"hooks":              a.setting(ctx, userHooksKey(u.ID), ""),
		"trusted_repo_hooks": len(trusted),
		"connections":        connOut,
		"mcp":                mcpOn,
		"automations":        autoOut,
		"tokens":             tokOut,
		"secret_names":       secretNames,
	})
}

func (a *App) adminUserHooksPut(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	var b struct {
		Hooks string `json:"hooks"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, err := agent.ParseHooks(b.Hooks, "user"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = a.Store.SetSetting(r.Context(), userHooksKey(u.ID), b.Hooks)
	a.adminAuditUser(r, u, "admin.user_hooks", fmt.Sprintf("%d bytes", len(b.Hooks)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserCommandsPut(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	var b struct {
		Commands []Command `json:"commands"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := validateCommands(b.Commands); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(b.Commands)
	_ = a.Store.SetSetting(r.Context(), userCommandsKey(u.ID), string(raw))
	a.adminAuditUser(r, u, "admin.user_commands", fmt.Sprintf("%d commands", len(b.Commands)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserNoteDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	nid, _ := strconv.ParseInt(chi.URLParam(r, "note"), 10, 64)
	if err := a.Store.DeleteMemory(r.Context(), u.ID, nid); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.adminAuditUser(r, u, "admin.user_note_delete", "note:"+itoa(nid))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserConnectionDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	if err := a.Store.DeleteConnection(r.Context(), u.ID, kind); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.adminAuditUser(r, u, "admin.user_connection_delete", kind)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserAutomationDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "aid")
	au, err := a.Store.GetAutomation(r.Context(), id)
	if err != nil || au.UserID != u.ID {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.DeleteAutomation(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.adminAuditUser(r, u, "admin.user_automation_delete", id+" "+au.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserTokenRevoke(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	tid, _ := strconv.ParseInt(chi.URLParam(r, "tid"), 10, 64)
	if err := a.Store.RevokeAPIToken(r.Context(), u.ID, tid, a.Now()); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.adminAuditUser(r, u, "admin.user_token_revoke", "token:"+itoa(tid))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) adminUserMCPSet(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminTargetUser(w, r)
	if !ok {
		return
	}
	var b struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	on := a.userMCP(r.Context(), u.ID)
	if b.Enabled {
		on[b.Name] = true
	} else {
		delete(on, b.Name)
	}
	var l []string
	for n := range on {
		l = append(l, n)
	}
	sort.Strings(l)
	raw, _ := json.Marshal(l)
	_ = a.Store.SetSetting(r.Context(), mcpUserKey(u.ID), string(raw))
	a.adminAuditUser(r, u, "admin.user_mcp", fmt.Sprintf("%s=%v", b.Name, b.Enabled))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
