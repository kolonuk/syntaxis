// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Any user can search the MCP catalogue and ask for a server. The admin sees the requests next to
// the approval screen, approves one (which clears it) or dismisses it. A request is only a
// request: nothing runs until an admin approves the server.
const (
	settingMCPRequests = "mcp_requests"
	maxMCPRequests     = 20 // open requests one user may have
)

// MCPRequest is one user's request for an MCP server to be approved.
type MCPRequest struct {
	ID        string    `json:"id"`
	UserID    int64     `json:"user_id"`
	User      string    `json:"user,omitempty"` // the user's email, filled in for the admin
	Name      string    `json:"name"`           // the catalogue name
	Title     string    `json:"title,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var mcpRequestsMu sync.Mutex

func (a *App) loadMCPRequests(ctx context.Context) []MCPRequest {
	raw, _ := a.Store.GetSetting(ctx, settingMCPRequests, "")
	var l []MCPRequest
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &l)
	}
	return l
}

func (a *App) saveMCPRequests(ctx context.Context, l []MCPRequest) error {
	b, _ := json.Marshal(l)
	return a.Store.SetSetting(ctx, settingMCPRequests, string(b))
}

// clearMCPRequests drops every request for a server, once it has been approved.
func (a *App) clearMCPRequests(ctx context.Context, name string) {
	mcpRequestsMu.Lock()
	defer mcpRequestsMu.Unlock()
	var keep []MCPRequest
	for _, r := range a.loadMCPRequests(ctx) {
		if r.Name != name {
			keep = append(keep, r)
		}
	}
	_ = a.saveMCPRequests(ctx, keep)
}

// handleMCPSearch lets any signed-in user browse the catalogue. It shows what a server is, not the
// reviewer's details, and marks the ones that are already approved or already asked for.
func (a *App) handleMCPSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	entries, next, err := a.mcpSource().Search(ctx, r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), 20)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "the catalogue is not available right now: "+err.Error())
		return
	}
	approved := map[string]bool{}
	if l, err := a.mcpApprovals().List(ctx); err == nil {
		for _, ap := range l {
			approved[ap.Name] = ap.Enabled
		}
	}
	asked := map[string]bool{}
	uid := a.user(r).ID
	for _, q := range a.loadMCPRequests(ctx) {
		if q.UserID == uid {
			asked[q.Name] = true
		}
	}
	out := []map[string]any{}
	for _, e := range entries {
		out = append(out, map[string]any{
			"name": e.Name, "title": e.Title, "description": e.Description, "repository": e.Repository,
			"approved": approved[e.Name], "requested": asked[e.Name],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "next": next})
}

// handleMyMCPRequests lists the signed-in user's open requests.
func (a *App) handleMyMCPRequests(w http.ResponseWriter, r *http.Request) {
	uid := a.user(r).ID
	out := []MCPRequest{}
	for _, q := range a.loadMCPRequests(r.Context()) {
		if q.UserID == uid {
			out = append(out, q)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMCPRequestAdd records a request. Asking again for the same server changes the reason.
func (a *App) handleMCPRequestAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name   string `json:"name"`
		Title  string `json:"title"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	b.Name, b.Title, b.Reason = strings.TrimSpace(b.Name), strings.TrimSpace(b.Title), strings.TrimSpace(b.Reason)
	if b.Name == "" || len(b.Name) > 200 || len(b.Title) > 200 || len(b.Reason) > 500 {
		writeErr(w, http.StatusBadRequest, "give the server's name (up to 200 characters) and, if you like, a reason (up to 500)")
		return
	}
	u := a.user(r)
	if l, err := a.mcpApprovals().List(r.Context()); err == nil {
		for _, ap := range l {
			if ap.Name == b.Name && ap.Enabled {
				writeErr(w, http.StatusConflict, "this server is already approved; switch it on under Tools")
				return
			}
		}
	}
	mcpRequestsMu.Lock()
	defer mcpRequestsMu.Unlock()
	l := a.loadMCPRequests(r.Context())
	open := 0
	for i := range l {
		if l[i].UserID != u.ID {
			continue
		}
		if l[i].Name == b.Name {
			l[i].Reason, l[i].Title = b.Reason, b.Title
			if err := a.saveMCPRequests(r.Context(), l); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, l[i])
			return
		}
		open++
	}
	if open >= maxMCPRequests {
		writeErr(w, http.StatusTooManyRequests, "you have too many open requests; withdraw one first")
		return
	}
	q := MCPRequest{ID: newID(), UserID: u.ID, Name: b.Name, Title: b.Title, Reason: b.Reason, CreatedAt: a.Now().UTC()}
	l = append(l, q)
	if err := a.saveMCPRequests(r.Context(), l); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, q)
}

// handleMCPRequestWithdraw lets a user remove their own request.
func (a *App) handleMCPRequestWithdraw(w http.ResponseWriter, r *http.Request) {
	a.dropMCPRequest(w, r, a.user(r).ID)
}

// adminMCPRequests lists every open request with the requester's email.
func (a *App) adminMCPRequests(w http.ResponseWriter, r *http.Request) {
	l := a.loadMCPRequests(r.Context())
	emails := map[int64]string{}
	for i := range l {
		if _, ok := emails[l[i].UserID]; !ok {
			if u, err := a.Store.GetUser(r.Context(), l[i].UserID); err == nil {
				emails[l[i].UserID] = u.Email
			}
		}
		l[i].User = emails[l[i].UserID]
	}
	if l == nil {
		l = []MCPRequest{}
	}
	writeJSON(w, http.StatusOK, l)
}

// adminMCPRequestDismiss removes a request without approving the server.
func (a *App) adminMCPRequestDismiss(w http.ResponseWriter, r *http.Request) {
	a.dropMCPRequest(w, r, 0)
}

// dropMCPRequest removes the request in the URL. owner 0 means an admin (any owner).
func (a *App) dropMCPRequest(w http.ResponseWriter, r *http.Request, owner int64) {
	id := chi.URLParam(r, "id")
	mcpRequestsMu.Lock()
	defer mcpRequestsMu.Unlock()
	var keep []MCPRequest
	found := false
	for _, q := range a.loadMCPRequests(r.Context()) {
		if q.ID == id && (owner == 0 || q.UserID == owner) {
			found = true
			continue
		}
		keep = append(keep, q)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no such request")
		return
	}
	if err := a.saveMCPRequests(r.Context(), keep); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
