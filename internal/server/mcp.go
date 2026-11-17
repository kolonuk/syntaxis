// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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
	// a credential comes either from an instance secret (shared by everyone who uses the server) or,
	// preferably, from each user's own connection ("user:NAME"), so the server runs as the user
	known := a.userCredentialNames()
	for _, sn := range b.EnvSecrets {
		if name, ok := strings.CutPrefix(sn, mcpUserPrefix); ok {
			if !known[name] {
				writeErr(w, http.StatusBadRequest, name+" is not a credential that users can connect")
				return
			}
			continue
		}
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

// ---- users: which approved servers they use ----

// mcpUserPrefix marks an approval's credential as coming from each user's own connection.
const mcpUserPrefix = "user:"

// userCredentialNames are the credential names users can connect (GITHUB_TOKEN, AWS_ACCESS_KEY_ID, ...).
func (a *App) userCredentialNames() map[string]bool {
	m := map[string]bool{}
	for _, k := range a.Connections.Kinds() {
		for _, f := range k.Fields {
			m[f.Name] = true
		}
	}
	return m
}

// adminMCPSuggestions lists first-party servers that run as a local program (not a hosted service),
// with the credentials they take mapped to the matching connection. GitLab and Cloudflare publish
// only hosted servers, which Syntaxis does not run.
func (a *App) adminMCPSuggestions(w http.ResponseWriter, _ *http.Request) {
	creds := []string{}
	for n := range a.userCredentialNames() {
		creds = append(creds, n)
	}
	sort.Strings(creds)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_credentials": creds,
		"suggestions": []map[string]any{
			{"title": "GitHub (official)", "search": "io.github.github/github-mcp-server", "env_secrets": map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": mcpUserPrefix + "GITHUB_TOKEN"}},
			{"title": "Microsoft Azure (official)", "search": "com.microsoft/azure", "env_secrets": map[string]string{
				"AZURE_TENANT_ID": mcpUserPrefix + "AZURE_TENANT_ID", "AZURE_CLIENT_ID": mcpUserPrefix + "AZURE_CLIENT_ID",
				"AZURE_CLIENT_SECRET": mcpUserPrefix + "AZURE_CLIENT_SECRET", "AZURE_SUBSCRIPTION_ID": mcpUserPrefix + "AZURE_SUBSCRIPTION_ID"}},
		},
	})
}

func mcpUserKey(userID int64) string { return "mcp_user:" + strconv.FormatInt(userID, 10) }

func (a *App) userMCP(ctx context.Context, userID int64) map[string]bool {
	raw, _ := a.Store.GetSetting(ctx, mcpUserKey(userID), "[]")
	var l []string
	_ = json.Unmarshal([]byte(raw), &l)
	m := map[string]bool{}
	for _, n := range l {
		m[n] = true
	}
	return m
}

// missingCredentials lists the user credentials an approved server needs that this user has not connected.
func (a *App) missingCredentials(ctx context.Context, userID int64, ap mcp.Approval) []string {
	env, _, _ := a.Secrets.Snapshot(ctx, userID)
	var missing []string
	for _, sn := range ap.EnvSecrets {
		if name, ok := strings.CutPrefix(sn, mcpUserPrefix); ok && env[name] == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// handleMyMCP lists the approved servers with whether this user has switched each on and can run it.
func (a *App) handleMyMCP(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	on := a.userMCP(r.Context(), u.ID)
	approved, _ := a.mcpApprovals().List(r.Context())
	out := []map[string]any{}
	for _, ap := range approved {
		if !ap.Enabled {
			continue
		}
		missing := a.missingCredentials(r.Context(), u.ID, ap)
		out = append(out, map[string]any{"name": ap.Name, "package": ap.Package.Identifier + "@" + ap.Package.Version, "enabled": on[ap.Name], "missing": missing, "ready": len(missing) == 0})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMyMCPSet switches one approved server on or off for the signed-in user only.
func (a *App) handleMyMCPSet(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	var b struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	approved, _ := a.mcpApprovals().List(r.Context())
	found := false
	for _, ap := range approved {
		if ap.Name == b.Name && ap.Enabled {
			found = true
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "that server is not approved")
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
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- a session's own on/off switches (within what the user has switched on) ----

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
	on := a.userMCP(r.Context(), s.UserID)
	approved, _ := a.mcpApprovals().List(r.Context())
	out := []map[string]any{}
	for _, ap := range approved {
		if ap.Enabled && on[ap.Name] {
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
func (a *App) startMCP(ctx context.Context, userID int64, sessionID string, emit func(agent.Event)) *mcp.Bridge {
	if a.Sandbox == nil {
		return nil
	}
	approved, err := a.mcpApprovals().List(ctx)
	if err != nil || len(approved) == 0 {
		return nil
	}
	off := a.disabledMCP(ctx, sessionID)
	on := a.userMCP(ctx, userID) // a server runs only for users who have switched it on
	userEnv, _, _ := a.Secrets.Snapshot(ctx, userID)
	b := mcp.NewBridge(a.Sandbox.RT, a.Sandbox.Config())
	// the servers run for this user: their egress mode applies to them as to their sandboxes
	mode := a.Cfg.EgressMode
	if raw, err := a.Store.GetUser(ctx, userID); err == nil {
		if eff := a.EffectiveUser(ctx, raw); eff.EgressMode != "" {
			mode = eff.EgressMode
		}
	}
	b.Owner = mcp.Owner{UserID: userID, NoNetwork: mode == "none"}
	for _, ap := range approved {
		if !ap.Enabled || off[ap.Name] || !on[ap.Name] {
			continue
		}
		if missing := a.missingCredentials(ctx, userID, ap); len(missing) > 0 {
			emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": "MCP server " + ap.Name + " was not started: it needs your " + strings.Join(missing, ", ") + " (Settings, Connections)."}})
			continue
		}
		vals := map[string]string{}
		for _, sn := range ap.EnvSecrets {
			if name, ok := strings.CutPrefix(sn, mcpUserPrefix); ok {
				vals[sn] = userEnv[name] // this user's own credential, never anyone else's
			} else if v, err := a.Secrets.Get(ctx, secrets.ScopeInstance, 0, sn); err == nil {
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
