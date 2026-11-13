// SPDX-License-Identifier: GPL-2.0-or-later

package quota

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/store"
)

var ctx = context.Background()

type fx struct {
	s   *Service
	st  *store.SQLite
	now time.Time
	u   *store.User
	def Defaults
}

func newFx(t *testing.T) *fx {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fx{st: st, now: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)}
	f.def = Defaults{DailyTokens: 1000, MonthlyTokens: 10000, MaxSessions: 2, RequestsPerMinute: 3, MaxParallelRequests: 2, SandboxSecondsDaily: 3600, MaxSessionWallClock: time.Hour}
	f.s = New(st, func() Defaults { return f.def })
	f.s.Now = func() time.Time { return f.now }
	id, _ := st.CreateAccount(ctx, store.Account{Kind: store.KindUser, Email: "u@example.com", Name: "u", CreatedAt: f.now})
	f.u, _ = st.GetUser(ctx, id)
	return f
}

func asExhausted(t *testing.T, err error, k Kind) *ExhaustedError {
	t.Helper()
	var ee *ExhaustedError
	if !errors.As(err, &ee) || ee.Kind != k {
		t.Fatalf("want %s exhausted, got %v", k, err)
	}
	return ee
}

func TestDailyTokenQuota(t *testing.T) {
	f := newFx(t)
	done, err := f.s.BeginModelCall(ctx, f.u, 100)
	if err != nil {
		t.Fatal(err)
	}
	done(600, 300)
	// 900 used; asking for 200 more would breach 1000
	_, err = f.s.BeginModelCall(ctx, f.u, 200)
	ee := asExhausted(t, err, KindDailyTokens)
	if ee.Used != 900 || ee.Limit != 1000 || !strings.Contains(ee.Error(), "Daily token quota") {
		t.Fatalf("%+v %s", ee, ee)
	}
	if ee.ResetAt != time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("reset %v", ee.ResetAt)
	}
	// resets next day
	f.now = f.now.Add(24 * time.Hour)
	if _, err := f.s.BeginModelCall(ctx, f.u, 200); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

func TestMonthlyTokenQuota(t *testing.T) {
	f := newFx(t)
	f.def.RequestsPerMinute = 0
	_ = f.st.AddUsage(ctx, f.u.ID, "2026-11-01", 9500, 0, 0)
	_, err := f.s.BeginModelCall(ctx, f.u, 600)
	ee := asExhausted(t, err, KindMonthlyTokens)
	if !strings.Contains(ee.Error(), "Monthly") || ee.ResetAt.Day() != 1 || ee.ResetAt.Month() != time.December {
		t.Fatalf("%s %v", ee, ee.ResetAt)
	}
}

func TestUserOverridesAndUnlimited(t *testing.T) {
	f := newFx(t)
	f.u.QuotaDailyTokens = 50
	if _, err := f.s.BeginModelCall(ctx, f.u, 100); err == nil {
		t.Fatal("user limit ignored")
	}
	f.u.QuotaDailyTokens = Unlimited
	f.u.QuotaMonthlyTokens = Unlimited
	f.def.RequestsPerMinute = 0
	if _, err := f.s.BeginModelCall(ctx, f.u, 1_000_000); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	f.u.MaxSessions = Unlimited
	if err := f.s.CheckSandboxStart(ctx, f.u, 99); err != nil {
		t.Fatal("unlimited sessions")
	}
}

func TestRateLimit(t *testing.T) {
	f := newFx(t)
	f.def.MaxParallelRequests = 0
	for i := 0; i < 3; i++ {
		d, err := f.s.BeginModelCall(ctx, f.u, 1)
		if err != nil {
			t.Fatal(err)
		}
		d(0, 0)
	}
	_, err := f.s.BeginModelCall(ctx, f.u, 1)
	asExhausted(t, err, KindRate)
	f.now = f.now.Add(61 * time.Second)
	if _, err := f.s.BeginModelCall(ctx, f.u, 1); err != nil {
		t.Fatalf("window should slide: %v", err)
	}
}

func TestRateLimitPerUser(t *testing.T) {
	f := newFx(t)
	f.def.MaxParallelRequests = 0
	id2, _ := f.st.CreateAccount(ctx, store.Account{Kind: store.KindUser, Email: "b@example.com", Name: "b", CreatedAt: f.now})
	u2, _ := f.st.GetUser(ctx, id2)
	for i := 0; i < 3; i++ {
		d, _ := f.s.BeginModelCall(ctx, f.u, 1)
		d(0, 0)
	}
	if _, err := f.s.BeginModelCall(ctx, u2, 1); err != nil {
		t.Fatalf("another user throttled by user 1's rate: %v", err)
	}
}

func TestParallelLimitAndRelease(t *testing.T) {
	f := newFx(t)
	f.def.RequestsPerMinute = 0
	d1, _ := f.s.BeginModelCall(ctx, f.u, 1)
	d2, _ := f.s.BeginModelCall(ctx, f.u, 1)
	_, err := f.s.BeginModelCall(ctx, f.u, 1)
	asExhausted(t, err, KindParallel)
	d1(1, 1)
	d1(1, 1) // double-done must not release twice nor double count
	if _, err := f.s.BeginModelCall(ctx, f.u, 1); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
	_, err = f.s.BeginModelCall(ctx, f.u, 1)
	asExhausted(t, err, KindParallel)
	d2(0, 0)
	u, _ := f.st.GetUsage(ctx, f.u.ID, "2026-11-15")
	if u.TokensIn != 1 || u.TokensOut != 1 {
		t.Fatalf("usage double counted: %+v", u)
	}
}

func TestFailedCallStillReleasesSlot(t *testing.T) {
	f := newFx(t)
	f.def.RequestsPerMinute = 0
	f.def.MaxParallelRequests = 1
	d, _ := f.s.BeginModelCall(ctx, f.u, 1)
	d(0, 0) // provider error: zero usage
	if _, err := f.s.BeginModelCall(ctx, f.u, 1); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxLimits(t *testing.T) {
	f := newFx(t)
	if err := f.s.CheckSandboxStart(ctx, f.u, 1); err != nil {
		t.Fatal(err)
	}
	asExhausted(t, f.s.CheckSandboxStart(ctx, f.u, 2), KindSessions)
	_ = f.s.RecordSandboxTime(ctx, f.u.ID, 2*time.Hour)
	asExhausted(t, f.s.CheckSandboxStart(ctx, f.u, 0), KindSandboxTime)
	f.u.MaxSessions = 5
	if err := f.s.CheckSandboxStart(ctx, f.u, 4); err == nil || !strings.Contains(err.Error(), "sandbox time") {
		t.Fatalf("%v", err)
	}
}

func TestSessionWallClock(t *testing.T) {
	f := newFx(t)
	if f.s.SessionExpired(f.u, f.now.Add(-30*time.Minute)) {
		t.Fatal("early")
	}
	if !f.s.SessionExpired(f.u, f.now.Add(-2*time.Hour)) {
		t.Fatal("late")
	}
	f.def.MaxSessionWallClock = 0
	if f.s.SessionExpired(f.u, f.now.Add(-100*time.Hour)) {
		t.Fatal("zero = no cap")
	}
}

func TestMeters(t *testing.T) {
	f := newFx(t)
	_ = f.st.AddUsage(ctx, f.u.ID, "2026-11-15", 100, 50, 10)
	_ = f.st.AddUsage(ctx, f.u.ID, "2026-11-02", 400, 0, 0)
	m, err := f.s.Meters(ctx, f.u)
	if err != nil {
		t.Fatal(err)
	}
	if m.Daily.Used != 150 || m.Daily.Limit != 1000 || m.Monthly.Used != 550 || m.SandboxSeconds.Used != 10 {
		t.Fatalf("%+v", m)
	}
}

func TestAdminTopUpAndReset(t *testing.T) {
	f := newFx(t)
	f.def.RequestsPerMinute = 0
	_ = f.st.AddUsage(ctx, f.u.ID, "2026-11-15", 800, 150, 0)
	if _, err := f.s.BeginModelCall(ctx, f.u, 100); err == nil {
		t.Fatal("should be exhausted")
	}
	if err := f.s.TopUp(ctx, 1, f.u.ID, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BeginModelCall(ctx, f.u, 100); err != nil {
		t.Fatalf("after top-up: %v", err)
	}
	// top-up larger than usage cannot create negative usage
	if err := f.s.TopUp(ctx, 1, f.u.ID, 10_000); err != nil {
		t.Fatal(err)
	}
	d, _ := f.st.GetUsage(ctx, f.u.ID, "2026-11-15")
	if d.TokensIn < 0 || d.TokensOut < 0 {
		t.Fatalf("negative usage %+v", d)
	}
	_ = f.st.AddUsage(ctx, f.u.ID, "2026-11-15", 999, 0, 0)
	if err := f.s.Reset(ctx, 1, f.u.ID); err != nil {
		t.Fatal(err)
	}
	d, _ = f.st.GetUsage(ctx, f.u.ID, "2026-11-15")
	if d.TokensIn != 0 {
		t.Fatal("reset")
	}
	es, _ := f.st.ListAudit(ctx, 0)
	if len(es) != 3 || es[0].Action != "quota.topup" {
		t.Fatalf("admin quota actions must be audited: %+v", es)
	}
}

func TestErrorMessagesAreHumane(t *testing.T) {
	for _, k := range []Kind{KindDailyTokens, KindMonthlyTokens, KindRate, KindParallel, KindSessions, KindSandboxTime} {
		e := (&ExhaustedError{Kind: k, Limit: 5, Used: 5, ResetAt: time.Now()}).Error()
		if len(e) < 20 || strings.Contains(e, "%") {
			t.Errorf("%s: %q", k, e)
		}
	}
	if (&ExhaustedError{Kind: "x"}).Error() == "" {
		t.Fatal("fallback")
	}
}
