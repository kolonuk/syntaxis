// SPDX-License-Identifier: GPL-2.0-or-later

package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var bg = context.Background()

func mustEnqueue(t *testing.T, s *Scheduler, u int64, c Class) *Ticket {
	t.Helper()
	tk, err := s.Enqueue(u, c)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func started(tk *Ticket) bool {
	select {
	case <-tk.ready:
		return tk.err == nil
	default:
		return false
	}
}

// drain runs queued tickets one at a time (concurrency 1) and returns the
// order of (user) in which they started.
func drain(t *testing.T, s *Scheduler, tickets []*Ticket) []int64 {
	t.Helper()
	var order []int64
	left := append([]*Ticket(nil), tickets...)
	for len(left) > 0 {
		progressed := false
		for i, tk := range left {
			if started(tk) {
				order = append(order, tk.User)
				rel, err := tk.Wait(bg)
				if err != nil {
					t.Fatal(err)
				}
				rel()
				left = append(left[:i], left[i+1:]...)
				progressed = true
				break
			}
		}
		if !progressed {
			t.Fatal("deadlock: nothing startable")
		}
	}
	return order
}

func TestConcurrencyNeverExceeded(t *testing.T) {
	s := New(Options{Concurrency: 3})
	var cur, max int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tk, _ := s.Enqueue(int64(i%5), Interactive)
			rel, err := tk.Wait(bg)
			if err != nil {
				t.Error(err)
				return
			}
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			rel()
		}(i)
	}
	wg.Wait()
	if max > 3 {
		t.Fatalf("provider concurrency exceeded: %d > 3", max)
	}
	if s.Stats().MaxRunning > 3 || s.Stats().Started != 40 {
		t.Fatalf("%+v", s.Stats())
	}
}

func TestPriorityClasses(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 99, Interactive)
	relH, _ := holder.Wait(bg)
	bench := mustEnqueue(t, s, 1, Benchmark)
	agent := mustEnqueue(t, s, 1, Agent)
	inter := mustEnqueue(t, s, 1, Interactive)
	relH()
	order := drain(t, s, []*Ticket{bench, agent, inter})
	_ = order
	// verify by start times: interactive before agent before benchmark
	if !(inter.started.Before(agent.started) || inter.started.Equal(agent.started)) || !(agent.started.Before(bench.started) || agent.started.Equal(bench.started)) {
		t.Fatalf("priority violated: inter=%v agent=%v bench=%v", inter.started, agent.started, bench.started)
	}
	if inter.Class != Interactive || bench.Class.String() != "benchmark" {
		t.Fatal("class")
	}
}

func TestPriorityOrderExplicit(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 99, Interactive)
	relH, _ := holder.Wait(bg)
	var tks []*Ticket
	tks = append(tks, mustEnqueue(t, s, 1, Benchmark), mustEnqueue(t, s, 2, Agent), mustEnqueue(t, s, 3, Interactive))
	relH()
	var classes []Class
	for len(tks) > 0 {
		for i, tk := range tks {
			if started(tk) {
				classes = append(classes, tk.Class)
				rel, _ := tk.Wait(bg)
				rel()
				tks = append(tks[:i], tks[i+1:]...)
				break
			}
		}
	}
	if classes[0] != Interactive || classes[1] != Agent || classes[2] != Benchmark {
		t.Fatalf("order %v", classes)
	}
}

func TestFairShareRoundRobin(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 99, Agent)
	relH, _ := holder.Wait(bg)
	// user 1 floods first, user 2 and 3 each submit one afterwards
	var tks []*Ticket
	for i := 0; i < 5; i++ {
		tks = append(tks, mustEnqueue(t, s, 1, Agent))
	}
	tks = append(tks, mustEnqueue(t, s, 2, Agent), mustEnqueue(t, s, 3, Agent))
	relH()
	order := drain(t, s, tks)
	// the first three starts must cover all three users: one user's long run cannot starve others
	seen := map[int64]bool{}
	for _, u := range order[:3] {
		seen[u] = true
	}
	if len(seen) != 3 {
		t.Fatalf("starvation: first three starts were %v", order[:3])
	}
}

func TestFIFOWithinUser(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 9, Agent)
	relH, _ := holder.Wait(bg)
	a, b, c := mustEnqueue(t, s, 1, Agent), mustEnqueue(t, s, 1, Agent), mustEnqueue(t, s, 1, Agent)
	relH()
	drain(t, s, []*Ticket{a, b, c})
	if !(a.ID < b.ID && !b.started.Before(a.started) && !c.started.Before(b.started)) {
		t.Fatal("per-user FIFO violated")
	}
}

func TestPerUserCap(t *testing.T) {
	s := New(Options{Concurrency: 4, MaxPerUser: 1})
	a1 := mustEnqueue(t, s, 1, Agent)
	a2 := mustEnqueue(t, s, 1, Agent)
	b1 := mustEnqueue(t, s, 2, Agent)
	if !started(a1) || !started(b1) {
		t.Fatal("first requests per user should start")
	}
	if started(a2) {
		t.Fatal("user exceeded per-user parallel cap while other slots were free")
	}
	rel, _ := a1.Wait(bg)
	rel()
	if !started(a2) {
		t.Fatal("second request did not start after the first finished")
	}
}

func TestPositionAndETA(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 9, Agent)
	_, _ = holder.Wait(bg)
	a := mustEnqueue(t, s, 1, Agent)
	b := mustEnqueue(t, s, 2, Agent)
	c := mustEnqueue(t, s, 3, Agent)
	pa, ea := a.Position()
	pc, ec := c.Position()
	if pa != 0 || pc != 2 {
		t.Fatalf("positions %d %d", pa, pc)
	}
	if ea <= 0 || ec <= ea {
		t.Fatalf("eta must grow with position: %v %v", ea, ec)
	}
	if p, e := holder.Position(); p != 0 || e != 0 {
		t.Fatal("running ticket should report 0")
	}
	_ = b
}

func TestPositionZeroWhenFreeSlots(t *testing.T) {
	s := New(Options{Concurrency: 2})
	a := mustEnqueue(t, s, 1, Agent)
	if p, e := a.Position(); p != 0 || e != 0 {
		t.Fatalf("%d %v", p, e)
	}
}

func TestCancelWaiting(t *testing.T) {
	s := New(Options{Concurrency: 1})
	holder := mustEnqueue(t, s, 9, Agent)
	relH, _ := holder.Wait(bg)
	tk := mustEnqueue(t, s, 1, Agent)
	ctx, cancel := context.WithCancel(bg)
	errc := make(chan error, 1)
	go func() { _, err := tk.Wait(ctx); errc <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, ErrCanceled) {
		t.Fatalf("%v", err)
	}
	relH()
	if es, _ := s.Snapshot(); len(es) != 0 {
		t.Fatal("cancelled ticket still queued")
	}
	if s.Stats().Cancelled != 1 {
		t.Fatalf("%+v", s.Stats())
	}
	tk2 := mustEnqueue(t, s, 1, Agent)
	rel, _ := tk2.Wait(bg)
	rel()
	if tk2.Cancel() {
		t.Fatal("cancel after completion must be false")
	}
}

func TestCancelExplicit(t *testing.T) {
	s := New(Options{Concurrency: 1})
	h := mustEnqueue(t, s, 9, Agent)
	rel, _ := h.Wait(bg)
	defer rel()
	tk := mustEnqueue(t, s, 1, Agent)
	if !tk.Cancel() {
		t.Fatal("cancel failed")
	}
	if _, err := tk.Wait(bg); !errors.Is(err, ErrCanceled) {
		t.Fatalf("%v", err)
	}
}

func TestMaxWaitTimeout(t *testing.T) {
	s := New(Options{Concurrency: 1, MaxWait: 50 * time.Millisecond})
	h := mustEnqueue(t, s, 9, Agent)
	rel, _ := h.Wait(bg)
	defer rel()
	tk := mustEnqueue(t, s, 1, Agent)
	start := time.Now()
	if _, err := tk.Wait(bg); !errors.Is(err, ErrTimeout) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not honoured")
	}
	if s.Stats().TimedOut != 1 {
		t.Fatal("timeout not counted")
	}
}

func TestReleaseIdempotent(t *testing.T) {
	s := New(Options{Concurrency: 1})
	a := mustEnqueue(t, s, 1, Agent)
	rel, _ := a.Wait(bg)
	rel()
	rel() // must not free a slot twice
	b := mustEnqueue(t, s, 2, Agent)
	c := mustEnqueue(t, s, 3, Agent)
	if !started(b) || started(c) {
		t.Fatal("double release over-admitted")
	}
}

func TestAdminBumpAndCancelID(t *testing.T) {
	s := New(Options{Concurrency: 1})
	h := mustEnqueue(t, s, 9, Agent)
	relH, _ := h.Wait(bg)
	a := mustEnqueue(t, s, 1, Agent)
	b := mustEnqueue(t, s, 2, Agent)
	c := mustEnqueue(t, s, 3, Agent)
	if !s.Bump(c.ID) {
		t.Fatal("bump")
	}
	if s.Bump(9999) {
		t.Fatal("bump of unknown id")
	}
	if !s.CancelID(a.ID) {
		t.Fatal("admin cancel")
	}
	if s.CancelID(9999) {
		t.Fatal("cancel unknown")
	}
	es, running := s.Snapshot()
	if running != 1 || len(es) != 2 || es[0].ID != c.ID {
		t.Fatalf("snapshot %+v running=%d", es, running)
	}
	relH()
	if !started(c) || started(b) {
		t.Fatal("bumped ticket should run first")
	}
}

func TestSetConcurrencyAtRuntime(t *testing.T) {
	s := New(Options{Concurrency: 1})
	a := mustEnqueue(t, s, 1, Agent)
	b := mustEnqueue(t, s, 2, Agent)
	if started(b) {
		t.Fatal("b should wait")
	}
	_ = a
	s.SetConcurrency(2)
	if !started(b) {
		t.Fatal("raising concurrency should admit waiting work")
	}
	s.SetConcurrency(0) // clamps to 1
	if s.opt.Concurrency != 1 {
		t.Fatal("clamp")
	}
}

func TestCloseCancelsWaiters(t *testing.T) {
	s := New(Options{Concurrency: 1})
	h := mustEnqueue(t, s, 9, Agent)
	_, _ = h.Wait(bg)
	w := mustEnqueue(t, s, 1, Agent)
	s.Close()
	if _, err := w.Wait(bg); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := s.Enqueue(1, Agent); !errors.Is(err, ErrClosed) {
		t.Fatal("enqueue after close")
	}
}

func TestInvalidClassDefaultsToAgent(t *testing.T) {
	s := New(Options{Concurrency: 1})
	tk, _ := s.Enqueue(1, Class(42))
	if tk.Class != Agent {
		t.Fatalf("%v", tk.Class)
	}
}

func TestStatsWaitTracked(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New(Options{Concurrency: 1, Now: func() time.Time { return now }})
	h := mustEnqueue(t, s, 9, Agent)
	rel, _ := h.Wait(bg)
	w := mustEnqueue(t, s, 1, Agent)
	now = now.Add(7 * time.Second)
	rel()
	if !started(w) {
		t.Fatal("not started")
	}
	st := s.Stats()
	if st.MaxWait != 7*time.Second || st.TotalWait != 7*time.Second {
		t.Fatalf("%+v", st)
	}
}
