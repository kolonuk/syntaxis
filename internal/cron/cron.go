// SPDX-License-Identifier: GPL-2.0-or-later

// Package cron is a thin layer over github.com/robfig/cron: it parses the usual five-field schedule
// ("minute hour day-of-month month day-of-week", plus @hourly, @daily, @weekly and @monthly) and
// finds the next time it fires, in the time zone of the time you give it.
package cron

import (
	"time"

	rcron "github.com/robfig/cron/v3"
)

var parser = rcron.NewParser(rcron.Minute | rcron.Hour | rcron.Dom | rcron.Month | rcron.Dow | rcron.Descriptor)

// Schedule is a parsed schedule.
type Schedule struct {
	s    rcron.Schedule
	spec string
}

// Parse reads a schedule.
func Parse(spec string) (*Schedule, error) {
	s, err := parser.Parse(spec)
	if err != nil {
		return nil, err
	}
	return &Schedule{s: s, spec: spec}, nil
}

// String returns the schedule as written.
func (s *Schedule) String() string { return s.spec }

// Next returns the first time after t that matches, in t's location, or the zero time if there is
// none (for example 31 February).
func (s *Schedule) Next(t time.Time) time.Time { return s.s.Next(t) }

// Every returns the shortest gap between two firings, found by looking ahead, so callers can refuse
// schedules that run too often.
func (s *Schedule) Every(from time.Time) time.Duration {
	best := time.Duration(1<<62 - 1)
	t := s.Next(from)
	for i := 0; i < 200 && !t.IsZero(); i++ {
		n := s.Next(t)
		if n.IsZero() {
			break
		}
		if d := n.Sub(t); d < best {
			best = d
		}
		t = n
	}
	return best
}
