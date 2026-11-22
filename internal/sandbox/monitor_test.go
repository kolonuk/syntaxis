// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"testing"
	"time"

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

func TestDiskIsMeasuredMoreOftenNearTheLimit(t *testing.T) {
	const limit = 5 << 30
	for _, c := range []struct {
		used int64
		want time.Duration
	}{{0, time.Minute}, {limit / 10, time.Minute}, {limit / 2, 10 * time.Second}, {limit * 89 / 100, 10 * time.Second}, {limit * 9 / 10, 3 * time.Second}, {limit * 2, 3 * time.Second}} {
		if got := diskCheckEvery(time.Minute, c.used, limit); got != c.want {
			t.Errorf("%d of %d bytes: %v, want %v", c.used, limit, got, c.want)
		}
	}
	if diskCheckEvery(time.Minute, 100, 0) != time.Minute {
		t.Error("no limit means the normal rate")
	}
	if diskCheckEvery(time.Second, limit, limit) != time.Second {
		t.Error("a faster base rate must not be slowed down")
	}
}
