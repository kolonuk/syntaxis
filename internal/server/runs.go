// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
)

const replayCap = 2000

// Run is one live (or finished) agent run for a session. Runs are background
// jobs: they keep going when no browser is connected.
type Run struct {
	ID      string
	started int64 // when the run began (unix milliseconds); a reconnecting browser quotes it so numbers from an earlier run are not mixed with this one's

	mu        sync.Mutex
	events    []agent.Event
	seq       int // events emitted so far; events holds the last len(events) of them, so events[i] is number seq-len(events)+i+1
	subs      map[chan seqEvent]struct{}
	cancel    context.CancelFunc
	running   bool
	approvals map[string]chan bool
	answers   chan string
	done      chan struct{}
}

// seqEvent is an event with its number in the run (1 for the first). The browser sends the last number
// it saw when it reconnects, so a dropped connection loses nothing and nothing arrives twice.
type seqEvent struct {
	Seq   int
	Event agent.Event
}

// Runs tracks runs by session id.
type Runs struct {
	mu sync.Mutex
	m  map[string]*Run
}

// NewRuns returns an empty registry.
func NewRuns() *Runs { return &Runs{m: map[string]*Run{}} }

// Get returns a run or nil.
func (rs *Runs) Get(id string) *Run {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.m[id]
}

// Start launches fn as a background job unless one is already running.
func (rs *Runs) Start(id string, fn func(ctx context.Context, r *Run)) (*Run, bool) {
	rs.mu.Lock()
	r := rs.m[id]
	if r != nil && r.IsRunning() {
		rs.mu.Unlock()
		return r, false
	}
	ctx, cancel := context.WithCancel(context.Background()) // NOT tied to the HTTP request
	r = &Run{ID: id, started: time.Now().UnixMilli(), subs: map[chan seqEvent]struct{}{}, cancel: cancel, running: true, approvals: map[string]chan bool{}, answers: make(chan string, 1), done: make(chan struct{})}
	rs.m[id] = r
	rs.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			r.running = false
			r.mu.Unlock()
			cancel()
			close(r.done)
		}()
		fn(ctx, r)
	}()
	return r, true
}

// IsRunning reports whether the job is active.
func (r *Run) IsRunning() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.running }

// Wait blocks until the job ends.
func (r *Run) Wait() { <-r.done }

// Emit records and broadcasts an event. A subscriber that cannot keep up is disconnected (its channel
// is closed) rather than left to miss events: it reconnects and is sent what it missed from the record.
func (r *Run) Emit(e agent.Event) {
	r.mu.Lock()
	r.seq++
	r.events = append(r.events, e)
	if len(r.events) > replayCap {
		r.events = r.events[len(r.events)-replayCap:]
	}
	se := seqEvent{Seq: r.seq, Event: e}
	for ch := range r.subs {
		select {
		case ch <- se:
		default:
			delete(r.subs, ch)
			close(ch)
		}
	}
	r.mu.Unlock()
}

// Subscribe returns the recorded events after number after (0 for all of them) and a live channel.
// The channel is closed if the subscriber falls behind; reconnect with the last number seen.
func (r *Run) Subscribe(after int) (replay []seqEvent, ch chan seqEvent, unsubscribe func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := r.seq - len(r.events) + 1
	for i, e := range r.events {
		if n := first + i; n > after {
			replay = append(replay, seqEvent{Seq: n, Event: e})
		}
	}
	ch = make(chan seqEvent, 256)
	r.subs[ch] = struct{}{}
	return replay, ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// Stop cancels the run.
func (r *Run) Stop() { r.cancel() }

// Approve blocks the calling agent until Decide is called for callID.
func (r *Run) Approve(ctx context.Context, callID string) (bool, error) {
	ch := make(chan bool, 1)
	r.mu.Lock()
	r.approvals[callID] = ch
	r.mu.Unlock()
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Decide answers a pending approval; false if none is pending.
func (r *Run) Decide(callID string, ok bool) bool {
	r.mu.Lock()
	ch, found := r.approvals[callID]
	delete(r.approvals, callID)
	r.mu.Unlock()
	if found {
		ch <- ok
	}
	return found
}

// PendingApprovals lists call ids awaiting a decision.
func (r *Run) PendingApprovals() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id := range r.approvals {
		ids = append(ids, id)
	}
	return ids
}

// Ask waits for the user's answer.
func (r *Run) Ask(ctx context.Context) (string, error) {
	select {
	case a := <-r.answers:
		return a, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Answer delivers an answer to a waiting Ask.
func (r *Run) Answer(text string) bool {
	select {
	case r.answers <- text:
		return true
	default:
		return false
	}
}
