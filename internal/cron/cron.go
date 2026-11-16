// SPDX-License-Identifier: GPL-2.0-or-later

// Package cron parses the usual five-field schedule ("minute hour day-of-month month
// day-of-week") and finds the next time it fires. Fields accept *, numbers, ranges (1-5),
// lists (1,3,5) and steps (*/15, 10-30/5). Month and weekday names (jan, mon) work too.
// Day-of-week is 0-6 with 0 = Sunday (7 is also Sunday). When both day-of-month and
// day-of-week are restricted, a day matches if either does, as in classic cron.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed schedule.
type Schedule struct {
	min, hour, dom, month, dow uint64
	domAny, dowAny             bool
	spec                       string
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// Parse reads a five-field schedule. Shorthands @hourly, @daily, @weekly and @monthly work.
func Parse(spec string) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "@hourly":
		spec = "0 * * * *"
	case "@daily", "@midnight":
		spec = "0 0 * * *"
	case "@weekly":
		spec = "0 0 * * 0"
	case "@monthly":
		spec = "0 0 1 * *"
	}
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, errors.New("a schedule has five fields: minute hour day-of-month month day-of-week")
	}
	s := &Schedule{spec: spec}
	var err error
	if s.min, _, err = field(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.hour, _, err = field(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.dom, s.domAny, err = field(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if s.month, _, err = field(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.dow, s.dowAny, err = field(f[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday too
		s.dow |= 1
	}
	return s, nil
}

func field(f string, lo, hi int, names map[string]int) (mask uint64, any bool, err error) {
	if f == "*" {
		any = true
	}
	for _, part := range strings.Split(f, ",") {
		step := 1
		rng := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			step, err = strconv.Atoi(part[i+1:])
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("bad step in %q", part)
			}
			rng = part[:i]
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			p := strings.SplitN(rng, "-", 2)
			if a, err = num(p[0], names); err != nil {
				return 0, false, err
			}
			if b, err = num(p[1], names); err != nil {
				return 0, false, err
			}
		default:
			if a, err = num(rng, names); err != nil {
				return 0, false, err
			}
			b = a
			if strings.Contains(part, "/") {
				b = hi // "5/10" means from 5 to the end
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("%q is out of range (%d-%d)", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, any, nil
}

func num(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return v, nil
}

// String returns the schedule as written.
func (s *Schedule) String() string { return s.spec }

func (s *Schedule) dayOK(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dowOK
	case s.dowAny:
		return domOK
	}
	return domOK || dowOK
}

// Next returns the first time after t that matches, in t's location, or the zero time if there is
// none within five years (for example 31 February).
func (s *Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayOK(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if s.min&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

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
