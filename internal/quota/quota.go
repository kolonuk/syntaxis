// SPDX-License-Identifier: GPL-2.0-or-later

// Package quota enforces token, rate and time limits server-side, before each
// model call and sandbox start. Tokens are the primary fairness control (they
// map to GPU use); time controls protect the host.
package quota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/store"
)

// Unlimited marks a per-user limit as explicitly unlimited (0 = use default).
const Unlimited = -1

// Defaults apply when a user's own value is 0.
type Defaults struct {
	DailyTokens         int64
	MonthlyTokens       int64
	MaxSessions         int
	RequestsPerMinute   int
	MaxParallelRequests int
	SandboxSecondsDaily int64         // total sandbox time per day
	CPUSecondsDaily     int64         // CPU time (core-seconds) per day; 0 = unlimited
	MaxSessionWallClock time.Duration // 0 = none
}

// Limits is the resolved set for one user (0 = no limit).
type Limits struct {
	DailyTokens, MonthlyTokens int64
	MaxSessions                int
	RequestsPerMinute          int
	MaxParallelRequests        int
	SandboxSecondsDaily        int64
	CPUSecondsDaily            int64
	MaxSessionWallClock        time.Duration
}

// Kind of limit that was hit.
type Kind string

// Limit kinds.
const (
	KindDailyTokens   Kind = "daily_tokens"
	KindMonthlyTokens Kind = "monthly_tokens"
	KindRate          Kind = "rate"
	KindParallel      Kind = "parallel"
	KindSessions      Kind = "sessions"
	KindSandboxTime   Kind = "sandbox_time"
	KindCPUTime       Kind = "cpu_time"
)

// ExhaustedError is returned when a limit is reached.
type ExhaustedError struct {
	Kind    Kind
	Limit   int64
	Used    int64
	ResetAt time.Time
}

func (e *ExhaustedError) Error() string {
	switch e.Kind {
	case KindDailyTokens:
		return fmt.Sprintf("Daily token quota used up (%d of %d). It resets at %s UTC, or ask an admin for a top-up.", e.Used, e.Limit, e.ResetAt.Format("15:04"))
	case KindMonthlyTokens:
		return fmt.Sprintf("Monthly token quota used up (%d of %d). It resets on %s, or ask an admin for a top-up.", e.Used, e.Limit, e.ResetAt.Format("2 Jan"))
	case KindRate:
		return "You are sending requests too quickly. Please wait a moment."
	case KindParallel:
		return "You already have the maximum number of model requests running. Wait for one to finish."
	case KindSessions:
		return fmt.Sprintf("You have reached your limit of %d concurrent sessions. Stop or delete one first.", e.Limit)
	case KindSandboxTime:
		return "Daily sandbox time used up. It resets at midnight UTC."
	case KindCPUTime:
		return "Daily compute time used up. It resets at midnight UTC."
	}
	return "quota exhausted"
}

// Service enforces quotas.
type Service struct {
	Store    store.Store
	Defaults func() Defaults
	Now      func() time.Time

	mu       sync.Mutex
	recent   map[int64][]time.Time
	inflight map[int64]int
}

// New returns a Service.
func New(st store.Store, defaults func() Defaults) *Service {
	return &Service{Store: st, Defaults: defaults, Now: time.Now, recent: map[int64][]time.Time{}, inflight: map[int64]int{}}
}

func pick64(user, def int64) int64 {
	switch {
	case user == Unlimited:
		return 0
	case user > 0:
		return user
	}
	return def
}

// Resolve merges a user's policy with defaults.
func (s *Service) Resolve(u *store.User) Limits {
	d := s.Defaults()
	l := Limits{
		DailyTokens:         pick64(u.QuotaDailyTokens, d.DailyTokens),
		MonthlyTokens:       pick64(u.QuotaMonthlyTokens, d.MonthlyTokens),
		RequestsPerMinute:   d.RequestsPerMinute,
		MaxParallelRequests: d.MaxParallelRequests,
		SandboxSecondsDaily: d.SandboxSecondsDaily,
		CPUSecondsDaily:     d.CPUSecondsDaily,
		MaxSessionWallClock: d.MaxSessionWallClock,
		MaxSessions:         d.MaxSessions,
	}
	switch {
	case u.MaxSessions == Unlimited:
		l.MaxSessions = 0
	case u.MaxSessions > 0:
		l.MaxSessions = u.MaxSessions
	}
	return l
}

func (s *Service) day() string   { return s.Now().UTC().Format("2006-01-02") }
func (s *Service) month() string { return s.Now().UTC().Format("2006-01") }

// Meter is what the UI shows.
type Meter struct {
	Used, Limit int64 // Limit 0 = unlimited
}

// Meters are the user's current usage meters.
type Meters struct {
	Daily, Monthly, SandboxSeconds, CPUSeconds Meter
	DailyResetsAt, MonthlyResetsAt             time.Time
}

// Meters returns current usage against limits.
func (s *Service) Meters(ctx context.Context, u *store.User) (*Meters, error) {
	l := s.Resolve(u)
	d, err := s.Store.GetUsage(ctx, u.ID, s.day())
	if err != nil {
		return nil, err
	}
	m, err := s.Store.SumUsage(ctx, u.ID, s.month())
	if err != nil {
		return nil, err
	}
	now := s.Now().UTC()
	return &Meters{
		Daily:           Meter{d.TokensIn + d.TokensOut, l.DailyTokens},
		Monthly:         Meter{m.TokensIn + m.TokensOut, l.MonthlyTokens},
		SandboxSeconds:  Meter{d.SandboxSeconds, l.SandboxSecondsDaily},
		CPUSeconds:      Meter{d.CPUSeconds, l.CPUSecondsDaily},
		DailyResetsAt:   time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC),
		MonthlyResetsAt: time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// BeginModelCall checks token, rate and parallelism limits BEFORE a request
// enters the queue, reserving a parallel slot. Call the returned done func
// with actual usage when the call finishes (also on error, with zeros).
func (s *Service) BeginModelCall(ctx context.Context, u *store.User, estTokens int64) (done func(in, out int64), err error) {
	l := s.Resolve(u)
	mt, err := s.Meters(ctx, u)
	if err != nil {
		return nil, err
	}
	if l.DailyTokens > 0 && mt.Daily.Used+estTokens > l.DailyTokens {
		return nil, &ExhaustedError{Kind: KindDailyTokens, Limit: l.DailyTokens, Used: mt.Daily.Used, ResetAt: mt.DailyResetsAt}
	}
	if l.MonthlyTokens > 0 && mt.Monthly.Used+estTokens > l.MonthlyTokens {
		return nil, &ExhaustedError{Kind: KindMonthlyTokens, Limit: l.MonthlyTokens, Used: mt.Monthly.Used, ResetAt: mt.MonthlyResetsAt}
	}
	now := s.Now()
	s.mu.Lock()
	if l.RequestsPerMinute > 0 {
		cut := now.Add(-time.Minute)
		r := s.recent[u.ID][:0]
		for _, t := range s.recent[u.ID] {
			if t.After(cut) {
				r = append(r, t)
			}
		}
		s.recent[u.ID] = r
		if len(r) >= l.RequestsPerMinute {
			s.mu.Unlock()
			return nil, &ExhaustedError{Kind: KindRate, Limit: int64(l.RequestsPerMinute), Used: int64(len(r))}
		}
	}
	if l.MaxParallelRequests > 0 && s.inflight[u.ID] >= l.MaxParallelRequests {
		s.mu.Unlock()
		return nil, &ExhaustedError{Kind: KindParallel, Limit: int64(l.MaxParallelRequests), Used: int64(s.inflight[u.ID])}
	}
	s.recent[u.ID] = append(s.recent[u.ID], now)
	s.inflight[u.ID]++
	s.mu.Unlock()

	var once sync.Once
	return func(in, out int64) {
		once.Do(func() {
			s.mu.Lock()
			s.inflight[u.ID]--
			if s.inflight[u.ID] <= 0 {
				delete(s.inflight, u.ID)
			}
			s.mu.Unlock()
			if in > 0 || out > 0 {
				_ = s.Store.AddUsage(context.Background(), u.ID, s.day(), in, out, 0)
			}
		})
	}, nil
}

// CheckSandboxStart checks concurrent-session and sandbox-time limits before a sandbox starts.
func (s *Service) CheckSandboxStart(ctx context.Context, u *store.User, activeSessions int) error {
	l := s.Resolve(u)
	if l.MaxSessions > 0 && activeSessions >= l.MaxSessions {
		return &ExhaustedError{Kind: KindSessions, Limit: int64(l.MaxSessions), Used: int64(activeSessions)}
	}
	if l.SandboxSecondsDaily > 0 || l.CPUSecondsDaily > 0 {
		d, err := s.Store.GetUsage(ctx, u.ID, s.day())
		if err != nil {
			return err
		}
		if l.SandboxSecondsDaily > 0 && d.SandboxSeconds >= l.SandboxSecondsDaily {
			return &ExhaustedError{Kind: KindSandboxTime, Limit: l.SandboxSecondsDaily, Used: d.SandboxSeconds}
		}
		if l.CPUSecondsDaily > 0 && d.CPUSeconds >= l.CPUSecondsDaily {
			return &ExhaustedError{Kind: KindCPUTime, Limit: l.CPUSecondsDaily, Used: d.CPUSeconds}
		}
	}
	return nil
}

// RecordSandboxTime adds sandbox run time to today's usage.
func (s *Service) RecordSandboxTime(ctx context.Context, userID int64, d time.Duration) error {
	return s.Store.AddUsage(ctx, userID, s.day(), 0, 0, int64(d.Seconds()))
}

// RecordCPUTime adds CPU time used by a user's sandboxes to today's usage.
func (s *Service) RecordCPUTime(ctx context.Context, userID int64, seconds int64) error {
	return s.Store.AddCPUSeconds(ctx, userID, s.day(), seconds)
}

// CPUExhausted reports whether the user's daily compute allowance is used up.
func (s *Service) CPUExhausted(ctx context.Context, u *store.User) bool {
	l := s.Resolve(u)
	if l.CPUSecondsDaily <= 0 {
		return false
	}
	d, err := s.Store.GetUsage(ctx, u.ID, s.day())
	return err == nil && d.CPUSeconds >= l.CPUSecondsDaily
}

// SessionExpired reports whether a session exceeded its wall-clock cap.
func (s *Service) SessionExpired(u *store.User, started time.Time) bool {
	l := s.Resolve(u)
	return l.MaxSessionWallClock > 0 && s.Now().Sub(started) > l.MaxSessionWallClock
}

// TopUp grants extra tokens today by reducing today's recorded usage (admin top-up).
func (s *Service) TopUp(ctx context.Context, actorAdmin, userID int64, tokens int64) error {
	d, err := s.Store.GetUsage(ctx, userID, s.day())
	if err != nil {
		return err
	}
	remove := tokens
	if remove > d.TokensIn+d.TokensOut {
		remove = d.TokensIn + d.TokensOut
	}
	// apply as a negative adjustment, in then out
	dIn := remove
	if dIn > d.TokensIn {
		dIn = d.TokensIn
	}
	dOut := remove - dIn
	if err := s.Store.AddUsage(ctx, userID, s.day(), -dIn, -dOut, 0); err != nil {
		return err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("admin:%d", actorAdmin), "quota.topup", fmt.Sprintf("user:%d", userID), fmt.Sprintf("tokens=%d", tokens))
	return nil
}

// Reset clears today's usage for a user (admin action).
func (s *Service) Reset(ctx context.Context, actorAdmin, userID int64) error {
	if err := s.Store.ResetUsage(ctx, userID, s.day()); err != nil {
		return err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("admin:%d", actorAdmin), "quota.reset", fmt.Sprintf("user:%d", userID), s.day())
	return nil
}
