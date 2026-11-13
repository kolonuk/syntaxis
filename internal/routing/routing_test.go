// SPDX-License-Identifier: GPL-2.0-or-later

package routing

import (
	"errors"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/provider"
)

func snap(names ...string) Snapshot {
	s := Snapshot{LoadedSupported: true}
	for _, n := range names {
		s.Loaded = append(s.Loaded, provider.LoadedModel{Name: n})
	}
	return s
}

// Model names below are arbitrary placeholders; the package must treat them as opaque.
var prof = Profile{Enabled: true, Provider: "p", Main: "model-a", Helper: "model-b"}

func TestOffByDefault(t *testing.T) {
	var p Profile
	d := Decide(p, snap("model-a", "model-b"), TaskTitle, "session-model")
	if d.Routed || d.Model != "session-model" || d.Fallback {
		t.Fatalf("zero profile must not route: %+v", d)
	}
}

func TestRoutesOnlyHelperTasks(t *testing.T) {
	s := snap("model-a", "model-b")
	for _, task := range []Task{TaskTitle, TaskSummary, TaskCompaction} {
		if d := Decide(prof, s, task, "model-a"); !d.Routed || d.Model != "model-b" {
			t.Errorf("%s: %+v", task, d)
		}
	}
	for _, task := range []Task{TaskPlan, TaskEdit, TaskChat} {
		if d := Decide(prof, s, task, "model-a"); d.Routed || d.Model != "model-a" {
			t.Errorf("%s: %+v", task, d)
		}
	}
}

func TestNonResidentRefusedAndFallsBack(t *testing.T) {
	if err := CanEnable(prof, snap("model-a")); !errors.Is(err, ErrNotResident) || !strings.Contains(err.Error(), "model-b") {
		t.Fatalf("%v", err)
	}
	d := Decide(prof, snap("model-a"), TaskSummary, "model-a")
	if d.Routed || !d.Fallback || d.Model != "model-a" || d.Notice == "" {
		t.Fatalf("must fall back to the single session model and tell the admin: %+v", d)
	}
	// helper drops out mid-flight: next decision falls back, and recovers when resident again
	if d := Decide(prof, snap("model-a", "model-b"), TaskSummary, "model-a"); !d.Routed {
		t.Fatal("should route when resident")
	}
	if d := Decide(prof, snap(), TaskSummary, "model-a"); !d.Fallback {
		t.Fatal("nothing loaded")
	}
}

func TestUnreportableServersNeedAdminAssertion(t *testing.T) {
	s := Snapshot{LoadedSupported: false}
	if err := CanEnable(prof, s); !errors.Is(err, ErrNotResident) {
		t.Fatal("unasserted residency accepted")
	}
	p := prof
	p.AssertResident = true
	if err := CanEnable(p, s); err != nil {
		t.Fatal(err)
	}
	if d := Decide(p, s, TaskTitle, "model-a"); !d.Routed {
		t.Fatal("asserted residency should route")
	}
}

func TestProfileValidation(t *testing.T) {
	for _, p := range []Profile{{Main: "a", Helper: ""}, {Main: "", Helper: "b"}, {Main: "a", Helper: "a"}} {
		if err := CanEnable(p, snap("a", "b")); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestFitMaths(t *testing.T) {
	gib := int64(1 << 30)
	f := Fit{Weights: []int64{10 * gib, 2 * gib}, KVCache: []int64{3 * gib, 1 * gib}, Overhead: 1 * gib, Memory: 24 * gib}
	if f.Total() != 17*gib || !f.Fits() {
		t.Fatalf("%d", f.Total())
	}
	f.Memory = 17 * gib // exact: leaves no safety margin
	if f.Fits() {
		t.Fatal("no headroom should not fit")
	}
}
