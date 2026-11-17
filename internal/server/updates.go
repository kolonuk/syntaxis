// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/buildinfo"
	"github.com/kolonuk/syntaxis/internal/update"
)

// Updating happens on the Docker host, because only the host can pull images, rebuild and restart
// the containers. The admin asks for an update here; a small service on the host (scripts/update-agent.sh)
// polls for the request with the host token, runs scripts/update.sh, and reports how it went. Syntaxis
// never sends the host a command: the agent only accepts a version tag, "latest" or "edge" and builds
// the command itself.
//
//	GET  /internal/update         "none", or "request <target>" (the request is handed out once)
//	POST /internal/update/status  "<running|done|failed> <message>"

const (
	settingUpdateRequest = "update_request"
	settingUpdateStatus  = "update_status"
	agentFreshFor        = 2 * time.Minute
)

// UpdateStatus is what the admin app shows about the last request.
type UpdateStatus struct {
	State   string    `json:"state"` // requested | running | done | failed
	Target  string    `json:"target"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	By      string    `json:"by,omitempty"`
}

type updateAgent struct {
	mu   sync.Mutex
	seen time.Time
}

func (u *updateAgent) touch(t time.Time) { u.mu.Lock(); u.seen = t; u.mu.Unlock() }
func (u *updateAgent) lastSeen() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seen
}

func (a *App) updateSource() *update.Source {
	if a.Updates == nil {
		a.Updates = &update.Source{}
	}
	return a.Updates
}

func (a *App) updateStatus(ctx context.Context) UpdateStatus {
	var s UpdateStatus
	_ = json.Unmarshal([]byte(a.setting(ctx, settingUpdateStatus, "{}")), &s)
	return s
}

func (a *App) setUpdateStatus(ctx context.Context, s UpdateStatus) {
	b, _ := json.Marshal(s)
	_ = a.Store.SetSetting(ctx, settingUpdateStatus, string(b))
}

// isReleaseVersion says whether the running build is a published release (as opposed to a build of
// the main branch or a checkout).
func isReleaseVersion(v string) bool { return update.ValidTag(v) }

// adminUpdateInfo answers the Updates tab: what is running, what is available, and how the last
// request went. ?refresh=1 asks GitHub again instead of using the remembered answer.
func (a *App) adminUpdateInfo(w http.ResponseWriter, r *http.Request) {
	cur := buildinfo.Get()
	releases, edge, err := a.updateSource().Check(r.Context(), r.URL.Query().Get("refresh") == "1")
	out := map[string]any{"current": cur, "releases": releases, "repo": update.DefaultRepo}
	if releases == nil {
		out["releases"] = []update.Release{}
	}
	if err != nil {
		out["check_error"] = err.Error()
	}
	latest, haveLatest := update.Latest(releases)
	if haveLatest {
		out["latest"] = latest
		out["release_available"] = isReleaseVersion(cur.Version) && update.Newer(latest.Tag, cur.Version)
	}
	if edge.SHA != "" {
		out["edge"] = map[string]any{"sha": edge.SHA, "date": edge.Date, "subject": edge.Subject, "newer": cur.Commit != "" && !strings.HasPrefix(edge.SHA, cur.Commit) && !strings.HasPrefix(cur.Commit, edge.SHA)}
	}
	if isReleaseVersion(cur.Version) {
		out["mode"] = "release"
	} else {
		out["mode"] = "edge"
	}
	seen := a.updAgent.lastSeen()
	running := !seen.IsZero() && a.Now().Sub(seen) < agentFreshFor
	out["agent"] = map[string]any{"running": running, "manual": "sudo sh scripts/update.sh"}
	out["status"] = a.updateStatus(r.Context())
	writeJSON(w, 200, out)
}

// adminUpdateRequest asks the host to update. It needs the host's update service to be running.
func (a *App) adminUpdateRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Target string `json:"target"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !update.ValidTarget(b.Target) {
		writeErr(w, 400, "choose the latest release, a version, or the development branch (edge)")
		return
	}
	if b.Target == "latest" {
		rs, _, _ := a.updateSource().Check(r.Context(), false)
		l, ok := update.Latest(rs)
		if !ok {
			writeErr(w, 400, "no release has been published yet; choose the development branch or wait for one")
			return
		}
		b.Target = l.Tag
	}
	seen := a.updAgent.lastSeen()
	if seen.IsZero() || a.Now().Sub(seen) >= agentFreshFor {
		writeErr(w, 409, "the update service on the host is not running. Start it (systemctl start syntaxis-update-agent) or update by hand with: sudo sh scripts/update.sh")
		return
	}
	if st := a.updateStatus(r.Context()); st.State == "requested" || st.State == "running" {
		writeErr(w, 409, "an update is already in progress")
		return
	}
	now := a.Now()
	req, _ := json.Marshal(map[string]any{"target": b.Target, "at": now})
	_ = a.Store.SetSetting(r.Context(), settingUpdateRequest, string(req))
	a.setUpdateStatus(r.Context(), UpdateStatus{State: "requested", Target: b.Target, At: now, By: a.actor(r)})
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "update.request", "server", b.Target)
	writeJSON(w, http.StatusAccepted, map[string]string{"target": b.Target})
}

func (a *App) hostAuth(w http.ResponseWriter, r *http.Request) bool {
	tok := a.Cfg.FirewallToken
	if tok == "" {
		http.NotFound(w, r)
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// handleUpdatePoll is what the host's update service asks every few seconds. A request is handed out
// once, then counts as running.
func (a *App) handleUpdatePoll(w http.ResponseWriter, r *http.Request) {
	if !a.hostAuth(w, r) {
		return
	}
	a.updAgent.touch(a.Now())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	raw := a.setting(r.Context(), settingUpdateRequest, "")
	if raw == "" {
		_, _ = io.WriteString(w, "none\n")
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	_ = a.Store.SetSetting(r.Context(), settingUpdateRequest, "")
	if json.Unmarshal([]byte(raw), &req) != nil || !update.ValidTarget(req.Target) {
		_, _ = io.WriteString(w, "none\n")
		return
	}
	st := a.updateStatus(r.Context())
	st.State, st.At = "running", a.Now()
	a.setUpdateStatus(r.Context(), st)
	_, _ = io.WriteString(w, "request "+req.Target+"\n")
}

// handleUpdateStatus records how an update went, as reported by the host's update service.
func (a *App) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !a.hostAuth(w, r) {
		return
	}
	a.updAgent.touch(a.Now())
	b, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	state, msg, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if state != "running" && state != "done" && state != "failed" {
		writeErr(w, 400, "bad status")
		return
	}
	if len(msg) > 600 {
		msg = msg[len(msg)-600:] // the end of the output is what says why
	}
	st := a.updateStatus(r.Context())
	st.State, st.Message, st.At = state, strings.ToValidUTF8(msg, ""), a.Now()
	a.setUpdateStatus(r.Context(), st)
	_, _ = a.Store.AppendAudit(r.Context(), "host", "update."+state, "server", st.Target)
	w.WriteHeader(http.StatusNoContent)
}
