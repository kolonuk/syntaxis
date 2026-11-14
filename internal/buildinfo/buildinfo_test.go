// SPDX-License-Identifier: GPL-2.0-or-later

package buildinfo

import "testing"

func TestString(t *testing.T) {
	cases := []struct {
		in   Info
		want string
	}{
		{Info{Version: "v1.2.3", Commit: "abc1234"}, "v1.2.3 (abc1234)"},
		{Info{Version: "dev"}, "dev"},
		{Info{Version: "dev", Commit: "abc1234", Dirty: true}, "dev (abc1234, modified)"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%q != %q", got, c.want)
		}
	}
}

func TestGetShortensAndIgnoresUnknown(t *testing.T) {
	old := Commit
	defer func() { Commit = old }()
	Commit = "0123456789abcdef"
	if g := Get(); g.Commit != "0123456" || g.Go == "" {
		t.Fatalf("%+v", g)
	}
	Commit = "unknown"
	if g := Get(); g.Commit == "unknown" {
		t.Fatal("placeholder commit must not be shown")
	}
}
