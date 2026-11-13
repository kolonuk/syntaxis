// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/mcp"
	"github.com/kolonuk/syntaxis/internal/secrets"
)

func (a *App) mcpApprovals() *mcp.Approvals { return &mcp.Approvals{Store: a.Store, Now: a.Now} }

func (a *App) mcpSource() mcp.Source {
	if a.MCPSource != nil {
		return a.MCPSource
	}
	return &mcp.Registry{}
}

// adminMCPSearch browses the catalogue and attaches a review to each package.
func (a *App) adminMCPSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	entries, next, err := a.mcpSource().Search(ctx, r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), 20)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "catalogue unavailable: "+err.Error())
		return
	}
	type pkgReview struct {
		Index  int         `json:"index"`
		Pkg    mcp.Package `json:"package"`
		Review mcp.Review  `json:"review"`
	}
	out := []map[string]any{}
	for _, e := range entries {
		var pk []pkgReview
		for i, p := range e.Packages {
			rv, _ := mcp.Summarise(e, i)
			pk = append(pk, pkgReview{i, p, rv})
		}
		out = append(out, map[string]any{"name": e.Name, "title": e.Title, "description": e.Description, "version": e.Version, "repository": e.Repository, "status": e.Status, "remote_only": len(e.Packages) == 0 && len(e.Remotes) > 0, "packages": pk})
	}
	writeJSON(w, 200, map[string]any{"servers": out, "next": next})
}

// adminMCPApprove approves a server. The server details are re-fetched from the
// catalogue by exact name: the client only picks which package and which
// secrets to map, it cannot supply its own command.
func (a *App) adminMCPApprove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name       string            `json:"name"`
		Package    int               `json:"package"`
		EnvSecrets map[string]string `json:"env_secrets"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var found *mcp.Entry
	cursor := ""
	for page := 0; page < 5 && found == nil; page++ {
		es, next, err := a.mcpSource().Search(ctx, b.Name, cursor, 50)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "catalogue unavailable: "+err.Error())
			return
		}
		for i := range es {
			if es[i].Name == b.Name {
				found = &es[i]
				break
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if found == nil {
		writeErr(w, http.StatusNotFound, "server not found in the catalogue")
		return
	}
	// every mapped secret must exist as an instance secret
	for _, sn := range b.EnvSecrets {
		if _, err := a.Secrets.Get(ctx, secrets.ScopeInstance, 0, sn); err != nil {
			writeErr(w, http.StatusBadRequest, "instance secret "+sn+" does not exist; add it first")
			return
		}
	}
	if err := a.mcpApprovals().Approve(ctx, a.actor(r), *found, b.Package, b.EnvSecrets); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminMCPApproved(w http.ResponseWriter, r *http.Request) {
	l, err := a.mcpApprovals().List(r.Context())
	if err != nil {
		writeErr(w, 500, "could not load approvals")
		return
	}
	out := []map[string]any{}
	for _, ap := range l {
		out = append(out, map[string]any{"name": ap.Name, "package": ap.Package.Identifier + "@" + ap.Package.Version, "type": ap.Package.RegistryType, "env_secrets": ap.EnvSecrets, "approved_by": ap.ApprovedBy, "at": ap.At})
	}
	writeJSON(w, 200, out)
}

func (a *App) adminMCPRevoke(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if err := a.mcpApprovals().Revoke(r.Context(), a.actor(r), name); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// instance secrets (admin-owned: provider keys, MCP credentials). Names only.
func (a *App) adminSecretNames(w http.ResponseWriter, r *http.Request) {
	n, _ := a.Secrets.Names(r.Context(), secrets.ScopeInstance, 0)
	if n == nil {
		n = []string{}
	}
	writeJSON(w, 200, n)
}

func (a *App) adminSecretPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &b) || b.Value == "" {
		return
	}
	if err := a.Secrets.Put(r.Context(), secrets.ScopeInstance, 0, chi.URLParam(r, "name"), b.Value); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminSecretDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.Secrets.Delete(r.Context(), secrets.ScopeInstance, 0, chi.URLParam(r, "name")); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- users: which approved servers a session uses ----

func mcpOffKey(sessionID string) string { return "mcp_off:" + sessionID }

func (a *App) disabledMCP(ctx context.Context, sessionID string) map[string]bool {
	raw, _ := a.Store.GetSetting(ctx, mcpOffKey(sessionID), "[]")
	var l []string
	_ = json.Unmarshal([]byte(raw), &l)
	m := map[string]bool{}
	for _, n := range l {
		m[n] = true
	}
	return m
}

func (a *App) handleSessionMCP(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	off := a.disabledMCP(r.Context(), s.ID)
	approved, _ := a.mcpApprovals().List(r.Context())
	out := []map[string]any{}
	for _, ap := range approved {
		if ap.Enabled {
			out = append(out, map[string]any{"name": ap.Name, "enabled": !off[ap.Name]})
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) handleSessionMCPSet(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Disabled []string `json:"disabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	raw, _ := json.Marshal(b.Disabled)
	_ = a.Store.SetSetting(r.Context(), mcpOffKey(s.ID), string(raw))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// startMCP launches the approved servers a session may use and returns the
// bridge (nil when none). The caller must Stop it when the run ends.
func (a *App) startMCP(ctx context.Context, sessionID string, emit func(agent.Event)) *mcp.Bridge {
	if a.Sandbox == nil {
		return nil
	}
	approved, err := a.mcpApprovals().List(ctx)
	if err != nil || len(approved) == 0 {
		return nil
	}
	off := a.disabledMCP(ctx, sessionID)
	b := mcp.NewBridge(a.Sandbox.RT, a.Sandbox.Config())
	for _, ap := range approved {
		if !ap.Enabled || off[ap.Name] {
			continue
		}
		vals := map[string]string{}
		for _, sn := range ap.EnvSecrets {
			if v, err := a.Secrets.Get(ctx, secrets.ScopeInstance, 0, sn); err == nil {
				vals[sn] = v
			}
		}
		if err := b.Start(ctx, ap, vals); err != nil {
			emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": fmt.Sprintf("MCP server %s could not start: %s", ap.Name, strings.TrimSpace(err.Error()))}})
			continue
		}
		emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": "MCP server " + ap.Name + " is available."}})
	}
	if len(b.Names()) == 0 {
		return nil
	}
	return b
}
