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
				"disk_bytes": res.DiskBytes, "disk_limit": res.DiskLimit,
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
		msg = "Heads up: " + ev.Reason + "."
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

// workspaceFull reports whether the user's workspace is over its size limit. Sessions are then
// refused (a run would only make it worse) until the user frees space; the terminal stays open
// so they can. The size is re-measured first, so deleting files takes effect at once.
func (a *App) workspaceFull(ctx context.Context, userID int64, session string) (bool, int64, int64) {
	if a.Monitor == nil {
		return false, 0, 0
	}
	used, limit := a.Monitor.UserDisk(userID)
	if limit <= 0 || used < limit {
		return false, used, limit
	}
	a.Monitor.Remeasure(ctx, session)
	used, limit = a.Monitor.UserDisk(userID)
	return used >= limit, used, limit
}

func mib(n int64) int64 { return n >> 20 }

func workspaceFullMessage(used, limit int64) string {
	return fmt.Sprintf("Your workspace is full (%d MiB of %d MiB). Delete some files, for example in the terminal tab, then send your message again.", mib(used), mib(limit))
}
