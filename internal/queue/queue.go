// SPDX-License-Identifier: GPL-2.0-or-later

// Package queue schedules model calls in front of one provider. It never runs
// more than Concurrency calls at once, serves interactive work before
// autonomous agent work before benchmarks, and shares capacity fairly between
// users inside each class (round-robin).
package queue

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Class is a priority class; lower values run first.
type Class int

// Priority classes.
const (
	Interactive Class = iota
	Agent
	Benchmark
	numClasses
)

func (c Class) String() string {
	return [...]string{"interactive", "agent", "benchmark"}[c]
}

// Errors.
var (
	ErrCanceled = errors.New("queue: request cancelled")
	ErrTimeout  = errors.New("queue: waited too long for the model; please try again")
	ErrClosed   = errors.New("queue: scheduler closed")
)

// Options configure a Scheduler.
type Options struct {
	Concurrency int           // max in-flight calls (>=1)
	MaxPerUser  int           // max in-flight calls per user (0 = unlimited)
	MaxWait     time.Duration // 0 = unlimited
	Now         func() time.Time
}

// Ticket is a queued or running request.
type Ticket struct {
	ID    uint64
	User  int64
	Class Class

	s        *Scheduler
	enqueued time.Time
	started  time.Time
	boosted  bool
	state    ticketState
	ready    chan struct{}
	err      error
}

type ticketState int

const (
	waiting ticketState = iota
	running
	done
)

// Scheduler is a per-provider fair queue.
type Scheduler struct {
	opt Options

	mu       sync.Mutex
	nextID   uint64
	running  int
	perUser  map[int64]int
	closed   bool
	queues   [numClasses]classQueue
	avgRun   time.Duration // exponential moving average of run time
	stats    Stats
	rrCursor [numClasses]int
}

type classQueue struct {
	users []int64             // round-robin order of users with waiting tickets
	byUsr map[int64][]*Ticket // FIFO per user
}

// Stats are cumulative scheduler metrics.
type Stats struct {
	Enqueued   uint64
	Started    uint64
	Cancelled  uint64
	TimedOut   uint64
	TotalWait  time.Duration
	MaxWait    time.Duration
	MaxRunning int
}

// New returns a Scheduler.
func New(o Options) *Scheduler {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Scheduler{opt: o, perUser: map[int64]int{}, avgRun: 10 * time.Second}
	for i := range s.queues {
		s.queues[i].byUsr = map[int64][]*Ticket{}
	}
	return s
}

// SetConcurrency changes the in-flight limit at runtime (admin setting).
func (s *Scheduler) SetConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	s.opt.Concurrency = n
	s.dispatchLocked()
	s.mu.Unlock()
}

// Enqueue adds a request and returns its ticket immediately.
func (s *Scheduler) Enqueue(user int64, class Class) (*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if class < 0 || class >= numClasses {
		class = Agent
	}
	s.nextID++
	t := &Ticket{ID: s.nextID, User: user, Class: class, s: s, enqueued: s.opt.Now(), ready: make(chan struct{})}
	q := &s.queues[class]
	if len(q.byUsr[user]) == 0 {
		q.users = append(q.users, user)
	}
	q.byUsr[user] = append(q.byUsr[user], t)
	s.stats.Enqueued++
	s.dispatchLocked()
	return t, nil
}

// Wait blocks until the ticket may run. It returns a release func that MUST be
// called when the model call finishes. ctx cancellation or MaxWait removes the
// ticket from the queue.
func (t *Ticket) Wait(ctx context.Context) (release func(), err error) {
	var timeout <-chan time.Time
	if t.s.opt.MaxWait > 0 {
		tm := time.NewTimer(t.s.opt.MaxWait)
		defer tm.Stop()
		timeout = tm.C
	}
	select {
	case <-t.ready:
		if t.err != nil {
			return nil, t.err
		}
		return t.release, nil
	case <-ctx.Done():
		if t.s.cancel(t, ErrCanceled, false) {
			return nil, ErrCanceled
		}
	case <-timeout:
		if t.s.cancel(t, ErrTimeout, true) {
			return nil, ErrTimeout
		}
	}
	// it started (or was cancelled) concurrently; honour the outcome
	<-t.ready
	if t.err != nil {
		return nil, t.err
	}
	return t.release, nil
}

// Cancel removes a waiting ticket (user cancel button / admin cancel).
func (t *Ticket) Cancel() bool { return t.s.cancel(t, ErrCanceled, false) }

func (t *Ticket) release() {
	s := t.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.state != running {
		return
	}
	t.state = done
	s.running--
	s.perUser[t.User]--
	if s.perUser[t.User] <= 0 {
		delete(s.perUser, t.User)
	}
	run := s.opt.Now().Sub(t.started)
	s.avgRun = (s.avgRun*4 + run) / 5
	s.dispatchLocked()
}

func (s *Scheduler) cancel(t *Ticket, err error, timedOut bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.state != waiting {
		return false
	}
	s.removeLocked(t)
	t.state, t.err = done, err
	if timedOut {
		s.stats.TimedOut++
	} else {
		s.stats.Cancelled++
	}
	close(t.ready)
	return true
}

func (s *Scheduler) removeLocked(t *Ticket) {
	q := &s.queues[t.Class]
	l := q.byUsr[t.User]
	for i, x := range l {
		if x == t {
			l = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(l) == 0 {
		delete(q.byUsr, t.User)
		for i, u := range q.users {
			if u == t.User {
				q.users = append(q.users[:i], q.users[i+1:]...)
				if s.rrCursor[t.Class] > i {
					s.rrCursor[t.Class]--
				}
				break
			}
		}
		if n := len(q.users); n > 0 {
			s.rrCursor[t.Class] %= n
		} else {
			s.rrCursor[t.Class] = 0
		}
		return
	}
	q.byUsr[t.User] = l
}

// dispatchLocked starts as many waiting tickets as capacity allows.
func (s *Scheduler) dispatchLocked() {
	for s.running < s.opt.Concurrency {
		t := s.pickLocked()
		if t == nil {
			return
		}
		s.removeLocked(t)
		// round-robin: advance the cursor past the user just served
		q := &s.queues[t.Class]
		if n := len(q.users); n > 0 {
			idx := indexOf(q.users, t.User)
			if idx >= 0 {
				s.rrCursor[t.Class] = (idx + 1) % n
			}
		}
		t.state, t.started = running, s.opt.Now()
		s.running++
		s.perUser[t.User]++
		w := t.started.Sub(t.enqueued)
		s.stats.Started++
		s.stats.TotalWait += w
		if w > s.stats.MaxWait {
			s.stats.MaxWait = w
		}
		if s.running > s.stats.MaxRunning {
			s.stats.MaxRunning = s.running
		}
		close(t.ready)
	}
}

func indexOf(l []int64, u int64) int {
	for i, x := range l {
		if x == u {
			return i
		}
	}
	return -1
}

// pickLocked chooses the next ticket: lowest class first, round-robin over
// users within the class, skipping users at their per-user cap. Boosted
// tickets (admin bump) go first.
func (s *Scheduler) pickLocked() *Ticket {
	for c := Class(0); c < numClasses; c++ {
		q := &s.queues[c]
		if len(q.users) == 0 {
			continue
		}
		for _, u := range q.users {
			for _, t := range q.byUsr[u] {
				if t.boosted && s.userOKLocked(u) {
					return t
				}
			}
		}
		n := len(q.users)
		for i := 0; i < n; i++ {
			u := q.users[(s.rrCursor[c]+i)%n]
			if !s.userOKLocked(u) {
				continue
			}
			return q.byUsr[u][0]
		}
	}
	return nil
}

func (s *Scheduler) userOKLocked(u int64) bool {
	return s.opt.MaxPerUser <= 0 || s.perUser[u] < s.opt.MaxPerUser
}

// Position returns how many requests will start before t (0 = next or running)
// and an estimated wait. Running or finished tickets report 0.
func (t *Ticket) Position() (ahead int, eta time.Duration) {
	s := t.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.state != waiting {
		return 0, 0
	}
	// Free slots absorb the first waiting tickets; everything strictly ahead in
	// start order counts.
	order := s.startOrderLocked()
	for i, x := range order {
		if x == t {
			ahead = i
			break
		}
	}
	slots := s.opt.Concurrency
	busy := s.running
	if ahead+busy < slots {
		return ahead, 0
	}
	waves := (ahead + busy - slots) / slots
	return ahead, time.Duration(waves+1) * s.avgRun / 1
}

// startOrderLocked simulates dispatch order without mutating state.
func (s *Scheduler) startOrderLocked() []*Ticket {
	var out []*Ticket
	cursors := s.rrCursor
	type cq struct {
		users []int64
		by    map[int64][]*Ticket
	}
	var cp [numClasses]cq
	for c := range s.queues {
		cp[c].users = append([]int64(nil), s.queues[c].users...)
		cp[c].by = map[int64][]*Ticket{}
		for u, l := range s.queues[c].byUsr {
			cp[c].by[u] = append([]*Ticket(nil), l...)
		}
	}
	for c := Class(0); c < numClasses; c++ {
		q := &cp[c]
		// boosted first
		for _, u := range append([]int64(nil), q.users...) {
			var rest []*Ticket
			for _, t := range q.by[u] {
				if t.boosted {
					out = append(out, t)
				} else {
					rest = append(rest, t)
				}
			}
			q.by[u] = rest
		}
		for {
			var live []int64
			for _, u := range q.users {
				if len(q.by[u]) > 0 {
					live = append(live, u)
				}
			}
			if len(live) == 0 {
				break
			}
			q.users = live
			idx := cursors[c] % len(live)
			u := live[idx]
			out = append(out, q.by[u][0])
			q.by[u] = q.by[u][1:]
			cursors[c] = idx + 1
			if len(q.by[u]) == 0 {
				cursors[c] = idx // user removed; next user shifts into idx
			}
		}
	}
	return out
}

// Entry is an admin-visible queue row.
type Entry struct {
	ID       uint64
	User     int64
	Class    Class
	Waiting  bool
	WaitedMs int64
}

// Snapshot returns live queue contents (waiting and running counts).
func (s *Scheduler) Snapshot() (entries []Entry, running int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	for _, t := range s.startOrderLocked() {
		entries = append(entries, Entry{ID: t.ID, User: t.User, Class: t.Class, Waiting: true, WaitedMs: now.Sub(t.enqueued).Milliseconds()})
	}
	return entries, s.running
}

// Bump moves a waiting ticket to the front of its class (admin action).
func (s *Scheduler) Bump(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.queues {
		for _, l := range s.queues[c].byUsr {
			for _, t := range l {
				if t.ID == id && t.state == waiting {
					t.boosted = true
					s.dispatchLocked()
					return true
				}
			}
		}
	}
	return false
}

// CancelID cancels a waiting ticket by id (admin action).
func (s *Scheduler) CancelID(id uint64) bool {
	s.mu.Lock()
	var target *Ticket
	for c := range s.queues {
		for _, l := range s.queues[c].byUsr {
			for _, t := range l {
				if t.ID == id {
					target = t
				}
			}
		}
	}
	s.mu.Unlock()
	return target != nil && s.cancel(target, ErrCanceled, false)
}

// Stats returns cumulative metrics.
func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close rejects new work and cancels waiting tickets.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	var all []*Ticket
	for c := range s.queues {
		for _, l := range s.queues[c].byUsr {
			all = append(all, l...)
		}
	}
	s.mu.Unlock()
	for _, t := range all {
		s.cancel(t, ErrClosed, false)
	}
}
