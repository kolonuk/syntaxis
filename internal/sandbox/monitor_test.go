// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"sync"
	"testing"
	"time"
)

type monFixture struct {
	f      *Fake
	m      *Monitor
	id     string
	now    time.Time
	nanos  uint64
	mu     sync.Mutex
	cpu    int64
	events []Event
}

func newMonFixture(t *testing.T) *monFixture {
	f := NewFake()
	id, _ := f.Create(context.Background(), Spec{Labels: map[string]string{LabelManaged: "true", LabelUser: "7", LabelSession: "sess"}, NanoCPUs: 2e9})
	_ = f.Start(context.Background(), id)
	mf := &monFixture{f: f, id: id, now: time.Unix(1000, 0), nanos: 1e9}
	cfg := DefaultWatchdogConfig()
	cfg.WarnAfter, cfg.ThrottleAfter, cfg.PauseAfter, cfg.KillAfter, cfg.MinerAfter = 10*time.Second, 30*time.Second, 60*time.Second, 120*time.Second, 0
	mf.m = NewMonitor(f, NewWatchdog(cfg), 2, nil)
	mf.m.Interval = 5 * time.Second
	mf.m.Window = 6
	mf.m.OnCPU = func(_ int64, s int64) { mf.mu.Lock(); mf.cpu += s; mf.mu.Unlock() }
	mf.m.OnEvent = func(_ string, e Event) { mf.events = append(mf.events, e) }
	return mf
}

// step advances 5 s during which the container used cores*5 CPU-seconds.
func (mf *monFixture) step(cores float64) {
	mf.now = mf.now.Add(5 * time.Second)
	mf.nanos += uint64(cores * 5 * 1e9)
	mf.f.SetStats(mf.id, Stats{CPUNanos: mf.nanos, MemoryBytes: 1 << 20, MemoryLimit: 1 << 30, At: mf.now})
	mf.m.Tick(context.Background(), mf.now)
}

func TestMonitorMetersCPUSeconds(t *testing.T) {
	mf := newMonFixture(t)
	mf.step(0) // baseline
	for i := 0; i < 4; i++ {
		mf.step(1) // one full core for 20 s
	}
	if mf.cpu < 19 || mf.cpu > 20 {
		t.Fatalf("cpu seconds %d, want about 20", mf.cpu)
	}
	r, ok := mf.m.Snapshot("sess")
	if !ok || r.CPUPercent < 99 || r.CPUPercent > 101 || r.CPUSeconds < 19.9 {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestMonitorP90ShowsBurstsMedianIgnoresThem(t *testing.T) {
	mf := newMonFixture(t)
	mf.step(0)
	for _, c := range []float64{0.1, 0.1, 0.1, 0.1, 0.1, 2} {
		mf.step(c)
	}
	r, _ := mf.m.Snapshot("sess")
	if r.CPUMedian > 20 || r.CPUP90 < 100 {
		t.Fatalf("median %.0f p90 %.0f", r.CPUMedian, r.CPUP90)
	}
	if len(mf.events) != 0 {
		t.Fatalf("a short burst must not trigger anything: %+v", mf.events)
	}
}

func TestMonitorThrottlesSustainedLoadThenRestores(t *testing.T) {
	mf := newMonFixture(t)
	mf.step(0)
	for i := 0; i < 8; i++ {
		mf.step(2) // pegged at the cap
	}
	r, _ := mf.m.Snapshot("sess")
	if !r.Throttled || mf.f.Containers[mf.id].Spec.NanoCPUs != 1e9 {
		t.Fatalf("not throttled: %+v nano=%d events=%+v", r, mf.f.Containers[mf.id].Spec.NanoCPUs, mf.events)
	}
	for i := 0; i < 8; i++ {
		mf.step(0.1)
	}
	r, _ = mf.m.Snapshot("sess")
	if r.Throttled || mf.f.Containers[mf.id].Spec.NanoCPUs != 2e9 {
		t.Fatalf("not restored: %+v", r)
	}
}

func TestMonitorPausesWhenAllowanceUsedUp(t *testing.T) {
	mf := newMonFixture(t)
	mf.m.Exhausted = func(int64) bool { return true }
	mf.step(0)
	mf.step(0.5)
	if !mf.f.Containers[mf.id].Info.Paused {
		t.Fatal("sandbox kept running after the daily compute allowance ran out")
	}
}
