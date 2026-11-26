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

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/mcp"
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
		// ImagePresent is set for a container image: whether it is on the Docker host yet.
		ImagePresent *bool `json:"image_present,omitempty"`
	}
	out := []map[string]any{}
	for _, e := range entries {
		pk := []pkgReview{} // never null: the admin app maps over these lists
		for i, p := range e.Packages {
			rv, _ := mcp.Summarise(e, i)
			if p.Env == nil {
				p.Env = []mcp.EnvVar{}
			}
			if rv.Capabilities == nil {
				rv.Capabilities = []string{}
			}
			if rv.Warnings == nil {
				rv.Warnings = []string{}
			}
			if rv.NeedsCredential == nil {
				rv.NeedsCredential = []string{}
			}
			pr := pkgReview{Index: i, Pkg: p, Review: rv}
			if p.RegistryType == "oci" {
				if present, ok := a.imageOnHost(ctx, p.Identifier); ok {
					pr.ImagePresent = &present
				}
			}
			pk = append(pk, pr)
		}
		out = append(out, map[string]any{"name": e.Name, "title": e.Title, "description": e.Description, "version": e.Version, "repository": e.Repository, "status": e.Status, "remote_only": len(e.Packages) == 0 && len(e.Remotes) > 0, "packages": pk})
	}
	writeJSON(w, 200, map[string]any{"servers": out, "next": next})
}

// imageOnHost reports whether a container image is already on the Docker host. ok is false when
// that cannot be checked (no Docker connection). Syntaxis cannot download images itself, so a
// server shipped as an image only runs once an admin has pulled it there.
func (a *App) imageOnHost(ctx context.Context, image string) (present, ok bool) {
	if a.Sandbox == nil {
		return false, false
	}
	present, err := a.Sandbox.RT.ImagePresent(ctx, image)
	return present, err == nil
}

// adminMCPApprove approves a server. The server details are re-fetched from the
// catalogue by exact name: the client only picks which package and which credential name each
// env var takes (a credential in the user's own vault, never an instance secret), it cannot supply
// its own command.
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
	// every credential is the user's own: the admin only says which credential name each env var takes
	for _, c := range b.EnvSecrets {
		if !mcp.ValidCredentialName(strings.TrimPrefix(c, "user:")) {
			writeErr(w, http.StatusBadRequest, c+" is not a valid credential name")
			return
		}
	}
	if b.Package >= 0 && b.Package < len(found.Packages) && found.Packages[b.Package].RegistryType == "oci" {
		img := found.Packages[b.Package].Identifier
		if present, ok := a.imageOnHost(ctx, img); ok && !present {
			writeErr(w, http.StatusConflict, "this server is a container image and it is not on the Docker host yet. Run this on the host, then approve again: docker pull "+img)
			return
		}
	}
	if err := a.mcpApprovals().Approve(ctx, a.actor(r), *found, b.Package, b.EnvSecrets); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.clearMCPRequests(ctx, b.Name)
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
		out = append(out, map[string]any{"name": ap.Name, "package": ap.Package.Ref(), "type": ap.Package.RegistryType, "env_secrets": ap.EnvSecrets, "shared_before": ap.SharedBefore, "approved_by": ap.ApprovedBy, "at": ap.At})
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

// adminMCPSharedNoticeDismiss clears the note about approvals that used to take a shared secret.
func (a *App) adminMCPSharedNoticeDismiss(w http.ResponseWriter, r *http.Request) {
	if err := a.mcpApprovals().ClearSharedNotice(r.Context(), a.actor(r)); err != nil {
		writeErr(w, 500, "could not save")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- users: which approved servers they use ----

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
			{"title": "GitHub (official)", "description": "Read and work with repositories, issues and pull requests as each user, with their own GitHub connection.", "search": "io.github.github/github-mcp-server", "env_secrets": map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "GITHUB_TOKEN"}},
			{"title": "Microsoft Azure (official)", "description": "Look at and manage Azure resources with each user's own service principal.", "search": "com.microsoft/azure", "env_secrets": map[string]string{
				"AZURE_TENANT_ID": "AZURE_TENANT_ID", "AZURE_CLIENT_ID": "AZURE_CLIENT_ID",
				"AZURE_CLIENT_SECRET": "AZURE_CLIENT_SECRET", "AZURE_SUBSCRIPTION_ID": "AZURE_SUBSCRIPTION_ID"}},
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

// mcpCredentials resolves the credentials an approved server needs from ONE user's own vault (their
// connections and the secrets they added). It is the only source of an MCP server's environment:
// no instance-wide secret and no other user's value is ever read. missing names the credentials this
// user has not supplied.
func (a *App) mcpCredentials(ctx context.Context, userID int64, ap mcp.Approval) (values map[string]string, missing []string) {
	env, _, _ := a.Secrets.Snapshot(ctx, userID)
	values = map[string]string{}
	for _, name := range ap.EnvSecrets {
		if v := env[name]; v != "" {
			values[name] = v
		} else {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return values, missing
}

// missingCredentials lists the credentials an approved server needs that this user has not supplied.
func (a *App) missingCredentials(ctx context.Context, userID int64, ap mcp.Approval) []string {
	_, missing := a.mcpCredentials(ctx, userID, ap)
	return missing
}

// handleMyMCP lists the approved servers with whether this user has switched each on and can run it,
// and the credentials each needs from them (names only, and whether they have supplied it).
func (a *App) handleMyMCP(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	on := a.userMCP(r.Context(), u.ID)
	approved, _ := a.mcpApprovals().List(r.Context())
	out := []map[string]any{}
	for _, ap := range approved {
		if !ap.Enabled {
			continue
		}
		_, missing := a.mcpCredentials(r.Context(), u.ID, ap)
		isMissing := map[string]bool{}
		for _, m := range missing {
			isMissing[m] = true
		}
		creds := []map[string]any{}
		seen := map[string]bool{}
		for env, name := range ap.EnvSecrets {
			if seen[name] {
				continue
			}
			seen[name] = true
			creds = append(creds, map[string]any{"name": name, "env": env, "set": !isMissing[name]})
		}
		sort.Slice(creds, func(i, j int) bool { return creds[i]["name"].(string) < creds[j]["name"].(string) })
		out = append(out, map[string]any{"name": ap.Name, "package": ap.Package.Ref(), "enabled": on[ap.Name], "missing": missing, "ready": len(missing) == 0, "credentials": creds})
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
	var found *mcp.Approval
	for i := range approved {
		if approved[i].Name == b.Name && approved[i].Enabled {
			found = &approved[i]
		}
	}
	if found == nil {
		writeErr(w, http.StatusNotFound, "that server is not approved")
		return
	}
	if missing := a.missingCredentials(r.Context(), u.ID, *found); b.Enabled && len(missing) > 0 {
		writeErr(w, http.StatusConflict, "add your own "+strings.Join(missing, ", ")+" first (Settings, Tools, or Connections)")
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
		vals, missing := a.mcpCredentials(ctx, userID, ap) // this user's own credentials, never anyone else's
		if len(missing) > 0 {
			emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": "MCP server " + ap.Name + " was not started: it needs your " + strings.Join(missing, ", ") + " (Settings, Tools)."}})
			continue
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
