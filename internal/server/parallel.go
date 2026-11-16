package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/store"
)

// maxAttempts caps how many attempts one batch may start, whatever the hardware says.
const maxAttempts = 8

// hostShare is the part of the machine's memory parallel attempts may plan on; the rest is for
// Docker, the model server if it runs here, and the operating system.
const hostShare = 0.75

// Capacity says how many attempts can run side by side right now and what limits it.
type Capacity struct {
	Recommended int      `json:"recommended"` // would all run at once
	Max         int      `json:"max"`         // the most that is allowed (extra ones queue for the model)
	HostCPUs    int      `json:"host_cpus"`
	HostMemory  int64    `json:"host_memory_bytes"`
	Limits      []string `json:"limits"` // plain-language reasons, one per constraint that applies
	ModelLoaded *bool    `json:"model_loaded,omitempty"`
}

// capacity works out how many parallel attempts the user can start with the given model.
func (a *App) capacity(ctx context.Context, u *store.User, model string) Capacity {
	c := Capacity{Max: maxAttempts, Recommended: maxAttempts}
	lower := func(n *int, v int, why string) {
		if v < 0 {
			v = 0
		}
		if v < *n {
			*n = v
			c.Limits = append(c.Limits, why)
		}
	}
	if a.Sandbox == nil {
		c.Max, c.Recommended = 0, 0
		c.Limits = append(c.Limits, "sandboxes are not available on this server")
		return c
	}
	// what the machine can hold
	lim := a.Sandbox.Cfg.Limits
	if hi, err := a.Sandbox.RT.HostInfo(ctx); err == nil && hi.CPUs > 0 {
		c.HostCPUs, c.HostMemory = hi.CPUs, hi.MemoryBytes
		if lim.CPUs > 0 {
			lower(&c.Recommended, int(float64(hi.CPUs)/lim.CPUs), fmt.Sprintf("this machine has %d CPUs and each sandbox may use %.3g", hi.CPUs, lim.CPUs))
		}
		if lim.MemoryBytes > 0 && hi.MemoryBytes > 0 {
			lower(&c.Recommended, int(float64(hi.MemoryBytes)*hostShare/float64(lim.MemoryBytes)),
				fmt.Sprintf("this machine has %d GiB of memory and each sandbox may use %d GiB", hi.MemoryBytes>>30, lim.MemoryBytes>>30))
		}
	}
	// what the user may run
	if l := a.Quota.Resolve(u); l.MaxSessions > 0 {
		active := 0
		sessions, _ := a.Store.ListChatSessions(ctx, u.ID)
		for _, s := range sessions {
			if s.State == "running" || s.State == "needs_input" {
				active++
			}
		}
		left := l.MaxSessions - active
		lower(&c.Recommended, left, fmt.Sprintf("you may run %d sessions at once and %d are running", l.MaxSessions, active))
		lower(&c.Max, left, "")
	}
	// what the model can serve at once
	if pid, mn, ok := a.resolveModel(ctx, u, model); ok {
		if p, err := a.Store.GetProvider(ctx, pid); err == nil {
			conc := max1(p.Concurrency)
			lower(&c.Recommended, conc, fmt.Sprintf("the model server handles %d request(s) at a time; more attempts wait their turn", conc))
			if pr, err := a.ProviderFor(p); err == nil {
				if resident, known := modelResident(ctx, pr, mn); known {
					c.ModelLoaded = &resident
					if !resident {
						c.Limits = append(c.Limits, "the model is not loaded yet; the first attempt loads it and the rest wait for that")
					}
				}
			}
		}
	}
	if c.Recommended > c.Max {
		c.Recommended = c.Max
	}
	// drop the blank reasons added for Max
	kept := c.Limits[:0]
	for _, l := range c.Limits {
		if l != "" {
			kept = append(kept, l)
		}
	}
	c.Limits = kept
	return c
}

func (a *App) handleParallelCapacity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.capacity(r.Context(), a.user(r), r.URL.Query().Get("model")))
}

// ParallelParams start several attempts at one task.
type ParallelParams struct {
	SessionParams
	Count  int    `json:"count"`
	Prompt string `json:"prompt"`
}

func (a *App) handleParallelCreate(w http.ResponseWriter, r *http.Request) {
	var b ParallelParams
	if !decode(w, r, &b) {
		return
	}
	u := a.user(r)
	if strings.TrimSpace(b.Prompt) == "" {
		writeErr(w, 400, "say what the attempts should do")
		return
	}
	if b.Count < 2 {
		writeErr(w, 400, "start at least two attempts")
		return
	}
	cp := a.capacity(r.Context(), u, b.Model)
	if b.Count > cp.Max {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("at most %d attempts can be started now", cp.Max))
		return
	}
	batch := newID()
	base := strings.TrimSpace(b.Title)
	var out []map[string]any
	for i := 1; i <= b.Count; i++ {
		p := b.SessionParams
		p.batchID = batch
		if base == "" {
			base = strings.TrimSpace(b.Prompt)
			if len(base) > 40 {
				base = base[:40] + "…"
			}
		}
		p.Title = fmt.Sprintf("%s (attempt %d)", base, i)
		s, err := a.newSession(r.Context(), u, p)
		if err == nil {
			err = a.launchRun(r.Context(), &s, u, b.Prompt)
		}
		if err != nil {
			var se *statusError
			code, msg := 500, "could not start the attempts"
			if errors.As(err, &se) {
				code, msg = se.code, se.msg
			}
			// keep what did start; say how far it got
			writeJSON(w, code, map[string]any{"error": msg, "batch": batch, "started": len(out)})
			return
		}
		out = append(out, sessionJSON(s, a.Runs.Get(s.ID)))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"batch": batch, "sessions": out})
}

func (a *App) batchSessions(ctx context.Context, userID int64, batch string) []store.ChatSession {
	all, _ := a.Store.ListChatSessions(ctx, userID)
	var out []store.ChatSession
	for _, s := range all {
		if s.BatchID == batch {
			out = append(out, s)
		}
	}
	return out
}

func (a *App) handleParallelGet(w http.ResponseWriter, r *http.Request) {
	ss := a.batchSessions(r.Context(), a.user(r).ID, chi.URLParam(r, "batch"))
	if len(ss) == 0 {
		writeErr(w, 404, "not found")
		return
	}
	out := make([]map[string]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, sessionJSON(s, a.Runs.Get(s.ID)))
	}
	writeJSON(w, 200, map[string]any{"batch": chi.URLParam(r, "batch"), "sessions": out})
}

// handleParallelKeep keeps one attempt and removes the others (their branches stay in the
// repository only if they were pushed, which sessions do not do on their own).
func (a *App) handleParallelKeep(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session string `json:"session"`
	}
	if !decode(w, r, &b) {
		return
	}
	u := a.user(r)
	ss := a.batchSessions(r.Context(), u.ID, chi.URLParam(r, "batch"))
	found := false
	for _, s := range ss {
		if s.ID == b.Session {
			found = true
		}
	}
	if !found {
		writeErr(w, 404, "that attempt is not part of this batch")
		return
	}
	for _, s := range ss {
		if s.ID == b.Session {
			continue // its clone keeps the batch suffix, so its folder does not move
		}
		if run := a.Runs.Get(s.ID); run != nil {
			run.Stop()
		}
		if s.SandboxID != "" && a.Sandbox != nil {
			_ = a.Sandbox.Destroy(r.Context(), s.ID, s.SandboxID)
		}
		_ = a.Store.DeleteChatSession(r.Context(), s.ID)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
