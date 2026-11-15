// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReasonAllowance is the event reason used when a user's daily compute allowance runs out.
const ReasonAllowance = "your daily compute allowance is used up"

// Resources is what the UI shows about a session's sandbox.
type Resources struct {
	CPUPercent   float64 // latest sample, percent of one core
	CPUMedian    float64 // typical load over the window (what enforcement uses)
	CPUP90       float64 // busiest 10% of the window
	CPUCap       float64 // allowed cores right now (lower while throttled)
	CPUCapNormal float64
	MemoryBytes  int64
	MemoryLimit  int64
	Throttled    bool
	Paused       bool
	CPUSeconds   float64 // used since the sandbox started
	Samples      int
	At           time.Time
}

type track struct {
	user      int64
	id        string
	lastNanos uint64
	lastAt    time.Time
	samples   []float64
	acc       float64 // CPU seconds not yet reported
	total     float64
	throttled bool
	paused    bool
	pauses    int // times the watchdog (not the allowance) has paused this sandbox
	last      Stats
	checkedAt time.Time
	diskAt    time.Time
	disk      int64
}

// Monitor samples every running sandbox, tracks CPU use (so it can be metered
// and shown) and enforces the watchdog's limits. Enforcement looks at the
// median over a window rather than single readings, so a short compile burst
// is fine but a sustained load is throttled, then paused, then stopped.
type Monitor struct {
	RT       Runtime
	Watchdog *Watchdog
	Log      *slog.Logger
	Interval time.Duration // sampling period (default 5s)
	Window   int           // samples kept (default 24 = 2 minutes)
	CPUCap   float64       // cores each sandbox may use
	// ThrottleTo is the fraction of the cap a throttled sandbox gets (default 0.5).
	ThrottleTo float64
	// OnCPU receives whole CPU-seconds as they accrue.
	OnCPU func(user int64, seconds int64)
	// OnEvent is told about watchdog decisions (warn, throttle, pause, kill, restore).
	OnEvent func(session string, ev Event)
	// Exhausted reports whether the user's daily compute allowance is used up.
	Exhausted func(user int64) bool
	// DiskBytes measures a sandbox's workspace (nil = not checked). It runs at most every DiskEvery.
	DiskBytes func(ctx context.Context, containerID string) (int64, error)
	DiskEvery time.Duration // default 60s

	mu     sync.Mutex
	tracks map[string]*track
}

// NewMonitor returns a Monitor with defaults.
func NewMonitor(rt Runtime, wd *Watchdog, cpuCap float64, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{RT: rt, Watchdog: wd, Log: log, Interval: 5 * time.Second, Window: 24, CPUCap: cpuCap, ThrottleTo: 0.5, tracks: map[string]*track{}}
}

// Run samples until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.Tick(ctx, now)
		}
	}
}

// Tick takes one sample of every running sandbox.
func (m *Monitor) Tick(ctx context.Context, now time.Time) {
	cs, err := m.RT.List(ctx)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if !c.Running || c.Paused {
			continue
		}
		session := c.Labels[LabelSession]
		user, _ := strconv.ParseInt(c.Labels[LabelUser], 10, 64)
		if session == "" {
			continue
		}
		seen[session] = true
		st, err := m.RT.Stats(ctx, c.ID)
		if err != nil {
			continue
		}
		m.observe(ctx, session, user, c.ID, st, now)
	}
	m.mu.Lock()
	for s, t := range m.tracks {
		if !seen[s] && !t.paused {
			delete(m.tracks, s)
			m.Watchdog.Forget(s)
		}
	}
	m.mu.Unlock()
}

func (m *Monitor) observe(ctx context.Context, session string, user int64, id string, st Stats, now time.Time) {
	m.mu.Lock()
	t := m.tracks[session]
	if t == nil || t.id != id {
		t = &track{user: user, id: id}
		m.tracks[session] = t
	}
	if t.paused {
		// it was resumed after a pause: give it a fresh start rather than judging it on the old load
		t.paused = false
		t.samples = nil
		t.lastNanos = 0
		m.Watchdog.Forget(session)
	}
	pct := st.CPUPercent
	if st.CPUNanos > 0 {
		if t.lastNanos > 0 && now.After(t.lastAt) && st.CPUNanos >= t.lastNanos {
			used := float64(st.CPUNanos-t.lastNanos) / 1e9
			pct = used / now.Sub(t.lastAt).Seconds() * 100
			t.acc += used
			t.total += used
		} else {
			pct = 0
		}
		t.lastNanos, t.lastAt = st.CPUNanos, now
	}
	t.last = st
	t.samples = append(t.samples, pct)
	if len(t.samples) > m.Window {
		t.samples = t.samples[len(t.samples)-m.Window:]
	}
	median := percentile(t.samples, 50)
	whole := int64(t.acc)
	t.acc -= float64(whole)
	capNow := m.CPUCap
	if t.throttled {
		capNow = m.CPUCap * m.ThrottleTo
	}
	throttled := t.throttled
	checkQuota := m.Exhausted != nil && now.Sub(t.checkedAt) >= 30*time.Second
	if checkQuota {
		t.checkedAt = now
	}
	every := m.DiskEvery
	if every <= 0 {
		every = time.Minute
	}
	checkDisk := m.DiskBytes != nil && now.Sub(t.diskAt) >= every
	if checkDisk {
		t.diskAt = now
	}
	m.mu.Unlock()
	if checkDisk {
		if n, err := m.DiskBytes(ctx, id); err == nil {
			m.mu.Lock()
			t.disk = n
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	disk := t.disk
	m.mu.Unlock()

	if whole > 0 && m.OnCPU != nil {
		m.OnCPU(user, whole)
	}
	if checkQuota && m.Exhausted(user) {
		m.apply(ctx, session, id, Event{Session: session, User: user, Action: ActionPause, Reason: ReasonAllowance})
		return
	}
	// A throttled sandbox that has calmed down gets its full cap back.
	if throttled && m.CPUCap > 0 && median < 0.4*m.CPUCap*100 {
		if err := m.RT.UpdateCPU(ctx, id, m.CPUCap); err == nil {
			m.mu.Lock()
			t.throttled = false
			m.mu.Unlock()
			m.Watchdog.Forget(session)
			if m.OnEvent != nil {
				m.OnEvent(session, Event{Session: session, User: user, Action: ActionNone, Reason: "CPU use has settled; full speed restored"})
			}
		}
		return
	}
	obs := st
	obs.CPUPercent = median
	for _, ev := range m.Watchdog.Observe(session, user, obs, disk, capNow, now) {
		m.apply(ctx, session, id, ev)
	}
}

func (m *Monitor) apply(ctx context.Context, session, id string, ev Event) {
	switch ev.Action {
	case ActionThrottle:
		if err := m.RT.UpdateCPU(ctx, id, m.CPUCap*m.ThrottleTo); err != nil {
			m.Log.Warn("throttle failed", "session", session, "err", err)
		} else {
			m.mu.Lock()
			if t := m.tracks[session]; t != nil {
				t.throttled = true
			}
			m.mu.Unlock()
		}
	case ActionPause:
		m.mu.Lock()
		t := m.tracks[session]
		repeat := t != nil && t.pauses >= 1 && ev.Reason != ReasonAllowance
		if t != nil && ev.Reason != ReasonAllowance {
			t.pauses++
		}
		m.mu.Unlock()
		if repeat {
			// paused once already and overloaded again after being resumed: stop it
			ev = Event{Session: session, User: ev.User, Action: ActionKill, Reason: ev.Reason + " (again after being paused)"}
			_ = m.RT.Stop(ctx, id, time.Second)
			break
		}
		if err := m.RT.Pause(ctx, id); err == nil {
			m.mu.Lock()
			if t != nil {
				t.paused = true
			}
			m.mu.Unlock()
		}
	case ActionKill:
		_ = m.RT.Stop(ctx, id, time.Second)
	}
	if m.OnEvent != nil {
		m.OnEvent(session, ev)
	}
}

// Snapshot returns the latest numbers for a session, if it is being tracked.
func (m *Monitor) Snapshot(session string) (Resources, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tracks[session]
	if t == nil || len(t.samples) == 0 {
		return Resources{}, false
	}
	capNow := m.CPUCap
	if t.throttled {
		capNow = m.CPUCap * m.ThrottleTo
	}
	return Resources{
		CPUPercent: t.samples[len(t.samples)-1], CPUMedian: percentile(t.samples, 50), CPUP90: percentile(t.samples, 90),
		CPUCap: capNow, CPUCapNormal: m.CPUCap, MemoryBytes: t.last.MemoryBytes, MemoryLimit: t.last.MemoryLimit,
		Throttled: t.throttled, Paused: t.paused, CPUSeconds: t.total, Samples: len(t.samples), At: t.last.At,
	}, true
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1 // nearest rank
	if i < 0 {
		i = 0
	}
	return s[i]
}

// WorkspaceBytes measures the size of a sandbox's workspace with du, run inside the sandbox.
func WorkspaceBytes(ctx context.Context, rt Runtime, containerID string) (int64, error) {
	r, err := rt.Exec(ctx, containerID, ExecSpec{Cmd: []string{"du", "-sk", WorkspacePath}, WorkingDir: "/", Timeout: 30 * time.Second, MaxOutput: 4096})
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(r.Stdout)
	if len(fields) == 0 {
		return 0, fmt.Errorf("du gave no output (exit %d)", r.ExitCode)
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return kb * 1024, nil
}
