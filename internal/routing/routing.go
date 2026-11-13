// SPDX-License-Identifier: GPL-2.0-or-later

// Package routing implements optional helper-model routing. It is OFF by
// default and decides only from data discovered at runtime from the operator's
// own server. Nothing here knows any model name, size or GPU.
package routing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kolonuk/syntaxis/internal/provider"
)

// Task kinds that may use the helper model.
type Task string

// Tasks.
const (
	TaskTitle      Task = "title"
	TaskSummary    Task = "summary"
	TaskCompaction Task = "compaction"
	TaskPlan       Task = "plan"
	TaskEdit       Task = "edit"
	TaskChat       Task = "chat"
)

// Profile is the admin's routing configuration.
type Profile struct {
	Enabled  bool
	Provider string
	Main     string // main model name as listed by the provider
	Helper   string // helper model name
	// AssertResident is the admin's assertion for providers that cannot report
	// residency (llama.cpp, LiteLLM, generic OpenAI-compatible).
	AssertResident bool
}

// Snapshot is what the provider reports right now.
type Snapshot struct {
	Loaded          []provider.LoadedModel
	LoadedSupported bool
}

// Decision is where a task should run.
type Decision struct {
	Model    string
	Routed   bool   // true when the helper model is used
	Fallback bool   // routing is on but unusable: single-model fallback
	Notice   string // for the admin when Fallback
}

// ErrNotResident is returned by CanEnable when the profile would swap models.
var ErrNotResident = errors.New("routing: models are not co-resident")

func isHelperTask(t Task) bool { return t == TaskTitle || t == TaskSummary || t == TaskCompaction }

func loadedSet(s Snapshot) map[string]bool {
	m := map[string]bool{}
	for _, l := range s.Loaded {
		m[l.Name] = true
	}
	return m
}

// CanEnable reports whether a profile may be switched on. It refuses unless
// every model is currently resident (or the admin asserts residency on a
// server that cannot report it). Syntaxis never loads a model to make this true.
func CanEnable(p Profile, s Snapshot) error {
	if p.Main == "" || p.Helper == "" || p.Main == p.Helper {
		return errors.New("routing: choose two different models")
	}
	if !s.LoadedSupported {
		if p.AssertResident {
			return nil
		}
		return fmt.Errorf("%w: this server cannot report which models are loaded; the admin must assert that both fit in memory together", ErrNotResident)
	}
	loaded := loadedSet(s)
	var missing []string
	for _, m := range []string{p.Main, p.Helper} {
		if !loaded[m] {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: not currently loaded: %s", ErrNotResident, strings.Join(missing, ", "))
	}
	return nil
}

// Decide picks the model for a task. sessionModel is what the user selected.
// If routing is off, everything runs on sessionModel. If a routed model drops
// out of residency, it falls back to sessionModel automatically and says so.
func Decide(p Profile, s Snapshot, task Task, sessionModel string) Decision {
	if !p.Enabled || !isHelperTask(task) {
		return Decision{Model: sessionModel}
	}
	if err := CanEnable(p, s); err != nil {
		return Decision{Model: sessionModel, Fallback: true, Notice: "Model routing is paused and the session model is used for everything: " + err.Error()}
	}
	return Decision{Model: p.Helper, Routed: true}
}

// Fit is a memory estimate in bytes for the "do these fit together" maths.
// The numbers come from the operator's own server and models (for example
// /api/ps sizes). The formula is illustrative; see docs/pitfalls.md.
type Fit struct {
	Weights  []int64 // resident weight sizes
	KVCache  []int64 // per-model KV cache at the configured context and parallelism
	Overhead int64   // runtime/driver overhead
	Memory   int64   // total memory available to the server
}

// Total returns the estimated total need.
func (f Fit) Total() int64 {
	t := f.Overhead
	for _, w := range f.Weights {
		t += w
	}
	for _, k := range f.KVCache {
		t += k
	}
	return t
}

// Fits reports whether the estimate is within memory (with a safety margin).
func (f Fit) Fits() bool { return f.Total() <= f.Memory*95/100 }
