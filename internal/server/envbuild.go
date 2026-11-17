// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/environments"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Example environments are built only when an admin chooses them, and removed again when nobody has
// used them for a while. Building and removing images happens on the Docker host, by the same
// service that does updates (scripts/update-agent.sh), one task at a time:
//
//	GET  /internal/environments         "build <name>", "remove <name>" or "none" (handed out once)
//	POST /internal/environments/status  "<build|remove> <name> <running|done|failed> [message]"
//
// The service only builds a name that has a Dockerfile in images/templates.

const (
	settingEnvTasks    = "env_tasks"
	settingEnvStatus   = "env_status"
	settingEnvIdleDays = "environment_idle_days"
	defaultEnvIdleDays = 30
)

type envTask struct {
	Action string `json:"action"` // build | remove
	Name   string `json:"name"`
}

// EnvStatus is how the last build or removal of one environment went.
type EnvStatus struct {
	State   string    `json:"state"` // queued | building | removing | built | removed | failed
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

func (a *App) envTasks(ctx context.Context) []envTask {
	var t []envTask
	_ = json.Unmarshal([]byte(a.setting(ctx, settingEnvTasks, "[]")), &t)
	return t
}

func (a *App) setEnvTasks(ctx context.Context, t []envTask) {
	b, _ := json.Marshal(t)
	_ = a.Store.SetSetting(ctx, settingEnvTasks, string(b))
}

func (a *App) envStatus(ctx context.Context) map[string]EnvStatus {
	m := map[string]EnvStatus{}
	_ = json.Unmarshal([]byte(a.setting(ctx, settingEnvStatus, "{}")), &m)
	return m
}

func (a *App) setEnvStatus(ctx context.Context, name string, s EnvStatus) {
	m := a.envStatus(ctx)
	m[name] = s
	b, _ := json.Marshal(m)
	_ = a.Store.SetSetting(ctx, settingEnvStatus, string(b))
}

func (a *App) envIdleDays(ctx context.Context) int {
	n, err := strconv.Atoi(a.setting(ctx, settingEnvIdleDays, strconv.Itoa(defaultEnvIdleDays)))
	if err != nil || n < 0 {
		return defaultEnvIdleDays
	}
	return n
}

func (a *App) agentRunning() bool {
	seen := a.updAgent.lastSeen()
	return !seen.IsZero() && a.Now().Sub(seen) < agentFreshFor
}

// adminEnvCatalog lists the example environments with what is known about each.
func (a *App) adminEnvCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	byName := map[string]store.ImageTemplate{}
	ts, _ := a.Store.ListImageTemplates(ctx)
	for _, t := range ts {
		byName[t.Name] = t
	}
	status := a.envStatus(ctx)
	out := []map[string]any{}
	for _, e := range environments.Catalog {
		row := map[string]any{"name": e.Name, "adds": e.Adds, "size_gb": e.SizeGB, "minutes": e.Minutes, "state": "not built"}
		st := status[e.Name]
		t, have := byName[e.Name]
		switch {
		case st.State == "queued" || st.State == "building" || st.State == "removing":
			row["state"] = st.State
		case have && t.Image == environments.Image(e.Name) && a.imagePresent(ctx, t.Image):
			row["state"] = "built"
			row["managed"] = t.Managed
			if !t.LastUsed.IsZero() {
				row["last_used"] = t.LastUsed
			}
		case st.State == "failed":
			row["state"] = "failed"
		}
		if st.Message != "" && (st.State == "failed" || st.State == "building") {
			row["message"] = st.Message
		}
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"environments": out, "agent_running": a.agentRunning(), "idle_days": a.envIdleDays(ctx), "manual": "sh scripts/build-templates.sh <name>"})
}

func (a *App) enqueueEnv(w http.ResponseWriter, r *http.Request, action string) {
	name := chi.URLParam(r, "name")
	if _, ok := environments.Find(name); !ok {
		writeErr(w, 404, "not found")
		return
	}
	ctx := r.Context()
	if !a.agentRunning() {
		writeErr(w, 409, "the update service on the host is not running, so images cannot be built from here. Start it (systemctl start syntaxis-update-agent) or run on the host: sh scripts/build-templates.sh "+name)
		return
	}
	if st := a.envStatus(ctx)[name]; st.State == "queued" || st.State == "building" || st.State == "removing" {
		writeErr(w, 409, "that environment is already being worked on")
		return
	}
	if action == "remove" {
		if used, _ := a.Store.ImageInUse(ctx, environments.Image(name)); used {
			writeErr(w, 409, "sessions are still using this environment; remove them or wait")
			return
		}
	}
	a.setEnvTasks(ctx, append(a.envTasks(ctx), envTask{Action: action, Name: name}))
	a.setEnvStatus(ctx, name, EnvStatus{State: "queued", At: a.Now()})
	_, _ = a.Store.AppendAudit(ctx, a.actor(r), "environment."+action, "environment:"+name, "")
	writeJSON(w, http.StatusAccepted, map[string]string{"name": name})
}

func (a *App) adminEnvBuild(w http.ResponseWriter, r *http.Request)  { a.enqueueEnv(w, r, "build") }
func (a *App) adminEnvRemove(w http.ResponseWriter, r *http.Request) { a.enqueueEnv(w, r, "remove") }

// adminEnvSettings sets how many days an unused environment is kept (0 = for ever).
func (a *App) adminEnvSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IdleDays int `json:"idle_days"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.IdleDays < 0 || b.IdleDays > 3650 {
		writeErr(w, 400, "days must be between 0 and 3650")
		return
	}
	_ = a.Store.SetSetting(r.Context(), settingEnvIdleDays, strconv.Itoa(b.IdleDays))
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "environment.settings", "server", "idle_days="+strconv.Itoa(b.IdleDays))
	writeJSON(w, 200, map[string]int{"idle_days": b.IdleDays})
}

// handleEnvPoll hands the host service its next task, once.
func (a *App) handleEnvPoll(w http.ResponseWriter, r *http.Request) {
	if !a.hostAuth(w, r) {
		return
	}
	a.updAgent.touch(a.Now())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	tasks := a.envTasks(ctx)
	for len(tasks) > 0 {
		t := tasks[0]
		tasks = tasks[1:]
		a.setEnvTasks(ctx, tasks)
		if _, ok := environments.Find(t.Name); !ok || (t.Action != "build" && t.Action != "remove") {
			continue
		}
		state := "building"
		if t.Action == "remove" {
			state = "removing"
		}
		a.setEnvStatus(ctx, t.Name, EnvStatus{State: state, At: a.Now()})
		_, _ = io.WriteString(w, t.Action+" "+t.Name+"\n")
		return
	}
	_, _ = io.WriteString(w, "none\n")
}

// handleEnvStatus records how a build or removal went. A finished build is approved at once, with
// the devcontainer images the catalog says it can stand for.
func (a *App) handleEnvStatus(w http.ResponseWriter, r *http.Request) {
	if !a.hostAuth(w, r) {
		return
	}
	a.updAgent.touch(a.Now())
	b, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	f := strings.SplitN(strings.TrimSpace(string(b)), " ", 4)
	if len(f) < 3 || (f[0] != "build" && f[0] != "remove") || (f[2] != "running" && f[2] != "done" && f[2] != "failed") {
		writeErr(w, 400, "bad status")
		return
	}
	action, name, state, msg := f[0], f[1], f[2], ""
	if len(f) == 4 {
		msg = strings.ToValidUTF8(f[3], "")
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
	}
	e, ok := environments.Find(name)
	if !ok {
		writeErr(w, 404, "unknown environment")
		return
	}
	ctx := r.Context()
	now := a.Now()
	switch {
	case state == "running":
		return204(w)
		return
	case state == "failed":
		a.setEnvStatus(ctx, name, EnvStatus{State: "failed", Message: msg, At: now})
	case action == "build":
		_ = a.Store.UpsertImageTemplate(ctx, store.ImageTemplate{Name: name, Image: environments.Image(name), Matches: e.Matches, Description: e.Adds, Managed: true, LastUsed: now})
		a.setEnvStatus(ctx, name, EnvStatus{State: "built", At: now})
	default: // removed
		_ = a.Store.DeleteImageTemplate(ctx, name)
		a.setEnvStatus(ctx, name, EnvStatus{State: "removed", At: now})
	}
	_, _ = a.Store.AppendAudit(ctx, "host", "environment."+action+"."+state, "environment:"+name, "")
	return204(w)
}

func return204(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// environmentsTick removes built environments that nobody has used for the configured time. It
// leaves any that a session still has a sandbox in, and does nothing while the host service is away.
func (a *App) environmentsTick(ctx context.Context) {
	days := a.envIdleDays(ctx)
	if days == 0 || !a.agentRunning() {
		return
	}
	ts, _ := a.Store.ListImageTemplates(ctx)
	status := a.envStatus(ctx)
	for _, t := range ts {
		if !t.Managed || t.LastUsed.IsZero() || a.Now().Sub(t.LastUsed) < time.Duration(days)*24*time.Hour {
			continue
		}
		if st := status[t.Name].State; st == "queued" || st == "building" || st == "removing" {
			continue
		}
		if used, _ := a.Store.ImageInUse(ctx, t.Image); used {
			continue
		}
		a.setEnvTasks(ctx, append(a.envTasks(ctx), envTask{Action: "remove", Name: t.Name}))
		a.setEnvStatus(ctx, t.Name, EnvStatus{State: "queued", Message: "unused for " + strconv.Itoa(days) + " days", At: a.Now()})
		_, _ = a.Store.AppendAudit(ctx, "system", "environment.cleanup", "environment:"+t.Name, "unused")
	}
}

// RunEnvironments looks for unused environments to clean up until ctx ends.
func (a *App) RunEnvironments(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.environmentsTick(ctx)
		}
	}
}
