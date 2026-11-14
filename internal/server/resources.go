// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// handleResources reports live CPU and memory use for the caller's session.
func (a *App) handleResources(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	out := map[string]any{"active": false}
	if a.Monitor != nil {
		if res, ok := a.Monitor.Snapshot(s.ID); ok {
			out = map[string]any{
				"active": true, "cpu_percent": res.CPUPercent, "cpu_typical": res.CPUMedian, "cpu_busiest": res.CPUP90,
				"cpu_cores": res.CPUCap, "cpu_cores_normal": res.CPUCapNormal,
				"memory_bytes": res.MemoryBytes, "memory_limit": res.MemoryLimit,
				"throttled": res.Throttled, "paused": res.Paused, "cpu_seconds": res.CPUSeconds,
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// SandboxEvent tells the user (and the audit log) what the monitor did to their sandbox.
func (a *App) SandboxEvent(session string, ev sandbox.Event) {
	msg := ""
	switch ev.Action {
	case sandbox.ActionWarn:
		msg = "Your sandbox is using a lot of CPU (" + ev.Reason + ")."
	case sandbox.ActionThrottle:
		msg = "Your sandbox is being slowed down because it has been busy for a while (" + ev.Reason + ")."
	case sandbox.ActionPause:
		msg = "Your sandbox was paused: " + ev.Reason + ". Send a message to resume once the load is gone."
	case sandbox.ActionKill:
		msg = "Your sandbox was stopped: " + ev.Reason + "."
	case sandbox.ActionNone:
		msg = ev.Reason + "."
	}
	if msg == "" {
		return
	}
	if run := a.Runs.Get(session); run != nil && run.IsRunning() {
		run.Emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": msg}})
	}
	if ev.Action != sandbox.ActionNone {
		_, _ = a.Store.AppendAudit(context.Background(), fmt.Sprintf("user:%d", ev.User), "sandbox."+ev.Action.String(), "session:"+session, ev.Reason)
	}
}
