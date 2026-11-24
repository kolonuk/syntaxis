// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/buildinfo"
	"github.com/kolonuk/syntaxis/internal/update"
)

// Updating happens on the Docker host, because only the host can pull images, rebuild and restart
// the containers. The admin asks for an update here; a small service on the host (scripts/update-agent.sh)
// polls for the request with the host token, runs scripts/update.sh, and reports how it went. Syntaxis
// never sends the host a command: the agent only accepts a version tag, "stable", "preview" or "edge"
// and builds the command itself.
//
//	GET  /internal/update         "none", or "request <target> [stream]" (the request is handed out once);
//	                              the target is a version tag or "edge", the stream stable, preview or edge,
//	                              left out when the target already implies it (so an older agent still works)
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
	Stream  string    `json:"stream,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	By      string    `json:"by,omitempty"`
}

// agentProtocol is the version of the request format the host service understands. 1 (no header) is
// the one-word form "request <target>"; 2 also takes "request <target> <stream>".
const agentProtocol = 2

type updateAgent struct {
	mu    sync.Mutex
	seen  time.Time
	proto int
}

func (u *updateAgent) touch(t time.Time, proto int) {
	u.mu.Lock()
	u.seen = t
	if proto > 0 {
		u.proto = proto
	}
	u.mu.Unlock()
}
func (u *updateAgent) protocol() int { u.mu.Lock(); defer u.mu.Unlock(); return u.proto }
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
	// the streams an install can follow, and what each would install right now
	stream := update.StreamOf(cur.Version, os.Getenv("SYNTAXIS_CHANNEL"))
	out["stream"] = stream
	var streams []map[string]any
	for _, id := range update.Streams {
		m := map[string]any{"id": id, "current": id == stream}
		if id == update.StreamEdge {
			if edge.SHA != "" {
				m["edge"] = out["edge"]
			}
		} else if l, ok := update.LatestIn(releases, id); ok {
			m["latest"] = l
			m["newer"] = isReleaseVersion(cur.Version) && update.Newer(l.Tag, cur.Version)
			m["running"] = l.Tag == cur.Version
		}
		streams = append(streams, m)
	}
	out["streams"] = streams
	seen := a.updAgent.lastSeen()
	running := !seen.IsZero() && a.Now().Sub(seen) < agentFreshFor
	out["agent"] = map[string]any{"running": running, "manual": "sudo sh scripts/update.sh", "protocol": a.updAgent.protocol()}
	out["status"] = a.updateStatus(r.Context())
	writeJSON(w, 200, out)
}

// hostProtocol reads which request format the host service says it understands. A service from
// before streams sends no header and knows only the one-word form.
func hostProtocol(r *http.Request) int {
	n, err := strconv.Atoi(r.Header.Get("X-Syntaxis-Agent"))
	if err != nil || n < 1 || n > 99 {
		return 0
	}
	return n
}

// needsStreamWord says whether a request must name its stream: only when the target does not
// already imply it. The one-word form is all an older host service understands.
func needsStreamWord(target, stream string) bool {
	return stream != "" && stream != impliedStream(target)
}

// requestLine is what is handed to the host service for a request.
func requestLine(target, stream string) string {
	if needsStreamWord(target, stream) {
		return "request " + target + " " + stream
	}
	return "request " + target
}

// resolveUpdateTarget turns what the admin chose into what the host is told: a version tag (or
// "edge") and the stream the install follows from now on. A stream name means "the newest release
// in it"; a tag with no stream follows the stream the tag belongs to.
func resolveUpdateTarget(rs []update.Release, target, stream string) (tag, strm string, err string) {
	if stream != "" && !update.ValidStream(stream) {
		return "", "", "unknown release stream"
	}
	if target == "latest" {
		target = update.StreamStable
	}
	switch {
	case update.ValidStream(target):
		if target == update.StreamEdge {
			return "edge", update.StreamEdge, ""
		}
		l, ok := update.LatestIn(rs, target)
		if !ok {
			return "", "", "no release is published in the " + target + " stream yet; choose another stream or wait for one"
		}
		return l.Tag, target, ""
	case update.ValidTag(target):
		if stream == update.StreamEdge {
			return "", "", "a version tag cannot be combined with the edge stream"
		}
		if stream == "" {
			stream = update.StreamStable
			if strings.Contains(target, "-") {
				stream = update.StreamPreview
			}
		}
		if stream == update.StreamStable && strings.Contains(target, "-") {
			return "", "", "that is a pre-release, so it belongs to the preview stream"
		}
		return target, stream, ""
	}
	return "", "", "choose a release stream (stable, preview or edge) or a version"
}

// impliedStream is the stream a request target belongs to when none is given.
func impliedStream(target string) string {
	switch {
	case target == "edge":
		return update.StreamEdge
	case strings.Contains(target, "-"):
		return update.StreamPreview
	}
	return update.StreamStable
}

// adminUpdateRequest asks the host to update. It needs the host's update service to be running.
func (a *App) adminUpdateRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Target string `json:"target"`
		Stream string `json:"stream"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !update.ValidTarget(b.Target) {
		writeErr(w, 400, "choose a release stream (stable, preview or edge) or a version")
		return
	}
	rs, _, _ := a.updateSource().Check(r.Context(), false)
	tag, strm, msg := resolveUpdateTarget(rs, b.Target, b.Stream)
	if msg != "" {
		writeErr(w, 400, msg)
		return
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
	// a host service from before streams cannot be told which stream to follow; it needs a restart,
	// which loads the version that is installed now
	if a.updAgent.protocol() < agentProtocol && needsStreamWord(tag, strm) {
		writeErr(w, 409, "the update service on the host is older than this version and cannot take that request. Restart it, then try again: sudo systemctl restart syntaxis-update-agent")
		return
	}
	now := a.Now()
	req, _ := json.Marshal(map[string]any{"target": tag, "stream": strm, "at": now})
	_ = a.Store.SetSetting(r.Context(), settingUpdateRequest, string(req))
	a.setUpdateStatus(r.Context(), UpdateStatus{State: "requested", Target: tag, Stream: strm, At: now, By: a.actor(r)})
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "update.request", "server", tag+" ("+strm+")")
	writeJSON(w, http.StatusAccepted, map[string]string{"target": tag, "stream": strm})
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
	a.updAgent.touch(a.Now(), hostProtocol(r))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	raw := a.setting(r.Context(), settingUpdateRequest, "")
	if raw == "" {
		_, _ = io.WriteString(w, "none\n")
		return
	}
	var req struct {
		Target string `json:"target"`
		Stream string `json:"stream"`
	}
	_ = a.Store.SetSetting(r.Context(), settingUpdateRequest, "")
	if json.Unmarshal([]byte(raw), &req) != nil || !(req.Target == "edge" || update.ValidTag(req.Target)) || (req.Stream != "" && !update.ValidStream(req.Stream)) {
		_, _ = io.WriteString(w, "none\n")
		return
	}
	st := a.updateStatus(r.Context())
	st.State, st.At = "running", a.Now()
	a.setUpdateStatus(r.Context(), st)
	_, _ = io.WriteString(w, requestLine(req.Target, req.Stream)+"\n")
}

// handleUpdateStatus records how an update went, as reported by the host's update service.
func (a *App) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !a.hostAuth(w, r) {
		return
	}
	a.updAgent.touch(a.Now(), hostProtocol(r))
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
