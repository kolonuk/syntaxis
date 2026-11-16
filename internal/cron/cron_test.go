// SPDX-License-Identifier: GPL-2.0-or-later

package cron

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	for _, c := range []struct{ spec, from, want string }{
		{"* * * * *", "2026-11-15 10:00", "2026-11-15 10:01"},
		{"*/15 * * * *", "2026-11-15 10:07", "2026-11-15 10:15"},
		{"0 9 * * *", "2026-11-15 10:00", "2026-11-16 09:00"},
		{"30 2 * * *", "2026-11-15 02:30", "2026-11-16 02:30"},
		{"0 9 * * mon", "2026-11-15 10:00", "2026-11-16 09:00"}, // 15 Nov 2026 is a Sunday
		{"0 9 * * 1-5", "2026-11-13 10:00", "2026-11-16 09:00"}, // Friday after nine -> Monday
		{"0 0 1 * *", "2026-11-15 10:00", "2026-12-01 00:00"},
		{"0 0 1 jan *", "2026-11-15 10:00", "2027-01-01 00:00"},
		{"0,30 8-10 * * *", "2026-11-15 08:30", "2026-11-15 09:00"},
		{"5/20 * * * *", "2026-11-15 10:06", "2026-11-15 10:25"},
		{"@daily", "2026-11-15 10:00", "2026-11-16 00:00"},
		{"@hourly", "2026-11-15 10:00", "2026-11-15 11:00"},
		// day-of-month OR day-of-week when both are restricted (classic cron)
		{"0 0 13 * fri", "2026-11-10 00:00", "2026-11-13 00:00"},
		{"0 0 13 * fri", "2026-11-14 00:00", "2026-11-20 00:00"},
		{"0 0 29 feb *", "2027-03-01 00:00", "2028-02-29 00:00"},
	} {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s from %s: got %s want %s", c.spec, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "a * * * *", "5-1 * * * *", "* * * * 8", "@sometimes"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestNeverFires(t *testing.T) {
	s, _ := Parse("0 0 31 feb *")
	if !s.Next(at("2026-01-01 00:00")).IsZero() {
		t.Fatal("31 February must never fire")
	}
}

func TestEveryFindsTheShortestGap(t *testing.T) {
	for spec, want := range map[string]time.Duration{"* * * * *": time.Minute, "*/10 * * * *": 10 * time.Minute, "0,5 * * * *": 5 * time.Minute, "0 9 * * *": 24 * time.Hour, "@hourly": time.Hour} {
		s, _ := Parse(spec)
		if got := s.Every(at("2026-11-15 10:00")); got != want {
			t.Errorf("%s: %v want %v", spec, got, want)
		}
	}
}

func TestTimezones(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	s, _ := Parse("0 9 * * *")
	from := time.Date(2026, 11, 15, 12, 0, 0, 0, loc)
	got := s.Next(from)
	if got.Hour() != 9 || got.Location() != loc || got.Day() != 16 {
		t.Fatalf("%v", got)
	}
}
