// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	xs := []float64{10, 10, 10, 10, 10, 200}
	if got := percentile(xs, 50); got != 10 {
		t.Fatalf("median %v: a short burst must not move it", got)
	}
	if got := percentile(xs, 90); got != 200 {
		t.Fatalf("p90 %v: the burst must show here", got)
	}
	if percentile(nil, 90) != 0 {
		t.Fatal("empty")
	}
}

// What the monitor does under sustained load, to the allowance and to the workspace size is tested
// on real gVisor containers in integration_test.go. One path cannot be driven by a real container
// without minutes of load: a sandbox that was paused, resumed, and overloaded a second time.
func TestMonitorSecondOverloadStopsInsteadOfPausing(t *testing.T) {
	testenv.NotTested(t, "pause, resume, then a second overload stopping the sandbox (needs several minutes of real load)")
}
