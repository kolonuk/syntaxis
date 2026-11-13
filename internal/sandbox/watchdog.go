// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Action is the watchdog's response to a sustained breach.
type Action int

// Actions, in escalating order.
const (
	ActionNone Action = iota
	ActionWarn
	ActionThrottle
	ActionPause
	ActionKill
)

func (a Action) String() string {
	return [...]string{"none", "warn", "throttle", "pause", "kill"}[a]
}

// WatchdogConfig sets thresholds. Zero values disable the matching check.
type WatchdogConfig struct {
	CPUPercentHigh    float64 // e.g. 95% of the CPU cap counts as high
	MemoryPercentHigh float64 // % of memory limit
	DiskWriteHigh     float64 // bytes/sec sustained
	DiskBytesLimit    int64   // workspace size cap
	PidsPercentHigh   float64 // % of pids limit (fork bomb)
	// escalation windows: how long a breach must persist
	WarnAfter     time.Duration
	ThrottleAfter time.Duration
	PauseAfter    time.Duration
	KillAfter     time.Duration
	// Miner heuristic: CPU at/above CPUPercentHigh for this long = abuse event.
	MinerAfter time.Duration
}

// DefaultWatchdogConfig returns defaults.
func DefaultWatchdogConfig() WatchdogConfig {
	return WatchdogConfig{CPUPercentHigh: 95, MemoryPercentHigh: 95, DiskWriteHigh: 100 << 20, DiskBytesLimit: 5 << 30, PidsPercentHigh: 90,
		WarnAfter: 30 * time.Second, ThrottleAfter: 2 * time.Minute, PauseAfter: 5 * time.Minute, KillAfter: 10 * time.Minute, MinerAfter: 30 * time.Minute}
}

// Event is something the watchdog decided.
type Event struct {
	Session string
	User    int64
	Action  Action
	Reason  string
	Abuse   string // non-empty when an abuse pattern was detected (admin email)
}

type breach struct {
	since time.Time
	last  Action
}

// Watchdog tracks sustained breaches per session.
type Watchdog struct {
	Cfg WatchdogConfig

	mu       sync.Mutex
	breaches map[string]map[string]*breach // session -> kind -> state
	minerAt  map[string]time.Time
	minerHit map[string]bool
}

// NewWatchdog returns a Watchdog.
func NewWatchdog(cfg WatchdogConfig) *Watchdog {
	return &Watchdog{Cfg: cfg, breaches: map[string]map[string]*breach{}, minerAt: map[string]time.Time{}, minerHit: map[string]bool{}}
}

// Observe feeds one sample and returns events. cpuCap is the CPU limit in
// cores (CPUPercent in Stats is relative to one core).
func (w *Watchdog) Observe(session string, user int64, st Stats, diskBytes int64, cpuCap float64, now time.Time) []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	var evs []Event
	cfg := w.Cfg

	type check struct {
		kind   string
		breach bool
		reason string
		hard   bool // immediate kill (disk full)
	}
	var checks []check
	if cfg.CPUPercentHigh > 0 && cpuCap > 0 {
		pct := st.CPUPercent / cpuCap
		checks = append(checks, check{"cpu", pct >= cfg.CPUPercentHigh, fmt.Sprintf("CPU at %.0f%% of its cap", pct), false})
	}
	if cfg.MemoryPercentHigh > 0 && st.MemoryLimit > 0 {
		pct := 100 * float64(st.MemoryBytes) / float64(st.MemoryLimit)
		checks = append(checks, check{"memory", pct >= cfg.MemoryPercentHigh, fmt.Sprintf("memory at %.0f%% of its limit", pct), false})
	}
	if cfg.DiskWriteHigh > 0 {
		checks = append(checks, check{"diskwrite", st.BlockWriteRate >= cfg.DiskWriteHigh, fmt.Sprintf("sustained disk writes of %.0f MB/s", st.BlockWriteRate/(1<<20)), false})
	}
	if cfg.PidsPercentHigh > 0 && st.PidsLimit > 0 {
		pct := 100 * float64(st.Pids) / float64(st.PidsLimit)
		checks = append(checks, check{"pids", pct >= cfg.PidsPercentHigh, fmt.Sprintf("process count at %.0f%% of its limit (fork bomb?)", pct), false})
	}
	if cfg.DiskBytesLimit > 0 {
		checks = append(checks, check{"disk", diskBytes >= cfg.DiskBytesLimit, fmt.Sprintf("workspace full (%d MiB of %d MiB)", diskBytes>>20, cfg.DiskBytesLimit>>20), true})
	}
	m := w.breaches[session]
	if m == nil {
		m = map[string]*breach{}
		w.breaches[session] = m
	}
	for _, c := range checks {
		b := m[c.kind]
		if !c.breach {
			delete(m, c.kind)
			continue
		}
		if b == nil {
			b = &breach{since: now}
			m[c.kind] = b
		}
		act := w.escalate(now.Sub(b.since))
		if c.hard {
			act = ActionKill
		}
		if act > b.last {
			b.last = act
			evs = append(evs, Event{Session: session, User: user, Action: act, Reason: c.reason})
		}
	}
	// miner heuristic: sustained full CPU
	cpuBreach := m["cpu"] != nil
	if cfg.MinerAfter > 0 && cpuBreach {
		if _, ok := w.minerAt[session]; !ok {
			w.minerAt[session] = now
		}
		if now.Sub(w.minerAt[session]) >= cfg.MinerAfter && !w.minerHit[session] {
			w.minerHit[session] = true
			evs = append(evs, Event{Session: session, User: user, Action: ActionWarn, Reason: "sustained full CPU", Abuse: "miner-like sustained 100% CPU for " + cfg.MinerAfter.String()})
		}
	} else {
		delete(w.minerAt, session)
		delete(w.minerHit, session)
	}
	return evs
}

func (w *Watchdog) escalate(d time.Duration) Action {
	c := w.Cfg
	switch {
	case c.KillAfter > 0 && d >= c.KillAfter:
		return ActionKill
	case c.PauseAfter > 0 && d >= c.PauseAfter:
		return ActionPause
	case c.ThrottleAfter > 0 && d >= c.ThrottleAfter:
		return ActionThrottle
	case c.WarnAfter > 0 && d >= c.WarnAfter:
		return ActionWarn
	}
	return ActionNone
}

// Forget drops state for a destroyed session.
func (w *Watchdog) Forget(session string) {
	w.mu.Lock()
	delete(w.breaches, session)
	delete(w.minerAt, session)
	delete(w.minerHit, session)
	w.mu.Unlock()
}

// Apply executes an action against a container. Throttle is implemented by
// the caller (cgroup CPU update); here it is a no-op hook.
func Apply(ctx context.Context, rt Runtime, containerID string, a Action) error {
	switch a {
	case ActionPause:
		return rt.Pause(ctx, containerID)
	case ActionKill:
		return rt.Stop(ctx, containerID, time.Second)
	}
	return nil
}
