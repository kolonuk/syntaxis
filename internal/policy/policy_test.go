// SPDX-License-Identifier: GPL-2.0-or-later

package policy

import (
	"reflect"
	"testing"

	"github.com/kolonuk/syntaxis/internal/store"
)

func TestMostSpecificNumberWins(t *testing.T) {
	level := &store.PolicyData{DailyTokens: 1000, MaxSessions: 5, EgressMode: "open", AllowedModels: []string{"a"}}
	group := &store.PolicyData{DailyTokens: 2000, EgressMode: "allowlist", AllowedModels: []string{"b"}}
	got := Apply(store.User{}, level, group)
	if got.QuotaDailyTokens != 2000 || got.MaxSessions != 5 || got.EgressMode != "allowlist" || !reflect.DeepEqual(got.AllowedModels, []string{"b"}) {
		t.Fatalf("group must beat level, level fills the rest: %+v", got)
	}
	own := store.User{QuotaDailyTokens: 50, EgressMode: "none", AllowedModels: []string{"c"}}
	got = Apply(own, level, group)
	if got.QuotaDailyTokens != 50 || got.EgressMode != "none" || !reflect.DeepEqual(got.AllowedModels, []string{"c"}) {
		t.Fatalf("a user's own settings must beat their group and level: %+v", got)
	}
}

func TestUnlimitedOnAUserOverridesALimit(t *testing.T) {
	got := Apply(store.User{QuotaDailyTokens: -1, MaxSessions: -1}, &store.PolicyData{DailyTokens: 100, MaxSessions: 2}, nil)
	if got.QuotaDailyTokens != -1 || got.MaxSessions != -1 {
		t.Fatalf("-1 (unlimited) is an explicit setting and must not be replaced: %+v", got)
	}
}

func TestPermissionsAreOffIfAnyLayerTurnsThemOff(t *testing.T) {
	cases := []struct {
		name                    string
		user                    store.User
		level, group            *store.PolicyData
		wantTerminal, wantCloud bool // disabled?
	}{
		{"nothing set", store.User{}, nil, nil, false, false},
		{"level turns terminal off", store.User{}, &store.PolicyData{NoTerminal: true}, nil, true, false},
		{"group turns cloud off", store.User{}, nil, &store.PolicyData{NoCloudTools: true}, false, true},
		{"user turns both off", store.User{TerminalDisabled: true, CloudToolsDisabled: true}, nil, nil, true, true},
		{"a user cannot switch back on what the group removed", store.User{TerminalDisabled: false}, nil, &store.PolicyData{NoTerminal: true}, true, false},
	}
	for _, c := range cases {
		got := Apply(c.user, c.level, c.group)
		if got.TerminalDisabled != c.wantTerminal || got.CloudToolsDisabled != c.wantCloud {
			t.Errorf("%s: terminal off=%v cloud off=%v", c.name, got.TerminalDisabled, got.CloudToolsDisabled)
		}
	}
}

func TestApplyDoesNotChangeTheInput(t *testing.T) {
	in := store.User{AllowedModels: []string{"x"}}
	_ = Apply(in, &store.PolicyData{NoTerminal: true}, nil)
	if in.TerminalDisabled {
		t.Fatal("the caller's user was modified")
	}
}

func TestValid(t *testing.T) {
	if Valid(store.PolicyData{EgressMode: "everything"}) == "" || Valid(store.PolicyData{DailyTokens: -5}) == "" {
		t.Fatal("bad values accepted")
	}
	if Valid(store.PolicyData{EgressMode: "allowlist", DailyTokens: -1}) != "" {
		t.Fatal("good values refused")
	}
}
