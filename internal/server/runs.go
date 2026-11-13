// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"sync"

	"github.com/kolonuk/syntaxis/internal/agent"
)

const replayCap = 2000

// Run is one live (or finished) agent run for a session. Runs are background
// jobs: they keep going when no browser is connected.
type Run struct {
	ID string

	mu        sync.Mutex
	events    []agent.Event
	subs      map[chan agent.Event]struct{}
	cancel    context.CancelFunc
	running   bool
	approvals map[string]chan bool
	answers   chan string
	done      chan struct{}
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
	r = &Run{ID: id, subs: map[chan agent.Event]struct{}{}, cancel: cancel, running: true, approvals: map[string]chan bool{}, answers: make(chan string, 1), done: make(chan struct{})}
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

// Emit records and broadcasts an event.
func (r *Run) Emit(e agent.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	if len(r.events) > replayCap {
		r.events = r.events[len(r.events)-replayCap:]
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default: // slow consumer: drop rather than block the agent
		}
	}
	r.mu.Unlock()
}

// Subscribe returns past events and a live channel.
func (r *Run) Subscribe() (replay []agent.Event, ch chan agent.Event, unsubscribe func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replay = append([]agent.Event(nil), r.events...)
	ch = make(chan agent.Event, 256)
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
