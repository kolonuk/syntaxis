// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"testing"
	"time"
)

func TestWatchdogEscalation(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	hot := Stats{CPUPercent: 200, MemoryBytes: 1, MemoryLimit: 100}
	want := map[time.Duration]Action{0: ActionNone, 30 * time.Second: ActionWarn, 2 * time.Minute: ActionThrottle, 5 * time.Minute: ActionPause, 10 * time.Minute: ActionKill}
	var got []Action
	for _, off := range []time.Duration{0, 10 * time.Second, 30 * time.Second, 31 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 11 * time.Minute} {
		for _, e := range w.Observe("s", 1, hot, 0, 2, t0.Add(off)) {
			got = append(got, e.Action)
			if want[off] != e.Action {
				t.Errorf("at %v got %v want %v", off, e.Action, want[off])
			}
		}
	}
	exp := []Action{ActionWarn, ActionThrottle, ActionPause, ActionKill}
	if len(got) != len(exp) {
		t.Fatalf("events %v want %v (each level must fire once)", got, exp)
	}
	for i := range exp {
		if got[i] != exp[i] {
			t.Fatalf("%v", got)
		}
	}
}

func TestWatchdogRecoveryResets(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	hot := Stats{CPUPercent: 200}
	calm := Stats{CPUPercent: 10}
	w.Observe("s", 1, hot, 0, 2, t0)
	if evs := w.Observe("s", 1, hot, 0, 2, t0.Add(40*time.Second)); len(evs) != 1 {
		t.Fatal("warn expected")
	}
	w.Observe("s", 1, calm, 0, 2, t0.Add(50*time.Second)) // recovers
	w.Observe("s", 1, hot, 0, 2, t0.Add(60*time.Second))  // breach restarts
	if evs := w.Observe("s", 1, hot, 0, 2, t0.Add(70*time.Second)); len(evs) != 0 {
		t.Fatalf("escalated without a sustained breach: %+v", evs)
	}
}

func TestWatchdogCPUCapRelative(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	// 100% of one core on a 4-core cap is only 25%: no breach
	w.Observe("s", 1, Stats{CPUPercent: 100}, 0, 4, t0)
	if evs := w.Observe("s", 1, Stats{CPUPercent: 100}, 0, 4, t0.Add(time.Hour)); len(evs) != 0 {
		t.Fatalf("%+v", evs)
	}
}

func TestWatchdogDiskFullKillsImmediately(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	evs := w.Observe("s", 1, Stats{}, 6<<30, 2, time.Unix(0, 0))
	if len(evs) != 1 || evs[0].Action != ActionKill {
		t.Fatalf("disk fill not contained: %+v", evs)
	}
}

func TestWatchdogForkBomb(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	s := Stats{Pids: 512, PidsLimit: 512}
	w.Observe("s", 1, s, 0, 2, t0)
	evs := w.Observe("s", 1, s, 0, 2, t0.Add(time.Minute))
	if len(evs) != 1 || evs[0].Action != ActionWarn || evs[0].Reason == "" {
		t.Fatalf("%+v", evs)
	}
}

func TestWatchdogMemoryAndDiskWrites(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	s := Stats{MemoryBytes: 99, MemoryLimit: 100, BlockWriteRate: 500 << 20}
	w.Observe("s", 1, s, 0, 2, t0)
	evs := w.Observe("s", 1, s, 0, 2, t0.Add(time.Minute))
	if len(evs) != 2 {
		t.Fatalf("want memory + diskwrite events, got %+v", evs)
	}
}

func TestWatchdogMinerDetection(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig())
	t0 := time.Unix(0, 0)
	hot := Stats{CPUPercent: 200}
	var abuse []Event
	for i := 0; i <= 40; i++ {
		for _, e := range w.Observe("s", 9, hot, 0, 2, t0.Add(time.Duration(i)*time.Minute)) {
			if e.Abuse != "" {
				abuse = append(abuse, e)
			}
		}
	}
	if len(abuse) != 1 || abuse[0].User != 9 {
		t.Fatalf("miner pattern should be reported exactly once: %+v", abuse)
	}
}

func TestWatchdogForgetAndDisabledChecks(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{})
	if evs := w.Observe("s", 1, Stats{CPUPercent: 999, MemoryBytes: 100, MemoryLimit: 100}, 1<<40, 2, time.Unix(0, 0)); len(evs) != 0 {
		t.Fatalf("zero config must disable checks: %+v", evs)
	}
	w2 := NewWatchdog(DefaultWatchdogConfig())
	w2.Observe("s", 1, Stats{CPUPercent: 200}, 0, 2, time.Unix(0, 0))
	w2.Forget("s")
	if len(w2.breaches) != 0 {
		t.Fatal("forget")
	}
}

func TestApplyActions(t *testing.T) {
	f := NewFake()
	m := NewManager(f, DefaultConfig(), nil)
	id, _ := m.Create(ctx, 1, "s", nil, false)
	_ = Apply(ctx, f, id, ActionWarn)
	if !f.Containers[id].Info.Running {
		t.Fatal("warn must not stop")
	}
	_ = Apply(ctx, f, id, ActionPause)
	if !f.Containers[id].Info.Paused {
		t.Fatal("pause")
	}
	_ = Apply(ctx, f, id, ActionKill)
	if f.Containers[id].Info.Running {
		t.Fatal("kill")
	}
	if ActionKill.String() != "kill" {
		t.Fatal("string")
	}
}
