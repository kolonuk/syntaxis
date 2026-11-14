// SPDX-License-Identifier: GPL-2.0-or-later

// Package buildinfo says which build is running, so a report can be matched to a
// release and a fix can be checked against the running app.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Set at build time:
//
//	-ldflags "-X github.com/kolonuk/syntaxis/internal/buildinfo.Version=v1.2.3 \
//	          -X github.com/kolonuk/syntaxis/internal/buildinfo.Commit=abc1234 \
//	          -X github.com/kolonuk/syntaxis/internal/buildinfo.Date=2026-11-14T10:00:00Z"
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info describes the running build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	Dirty   bool   `json:"dirty"`
}

// Get returns the build info, filling gaps from the Go toolchain's VCS stamp when
// the binary was built from a git checkout without the linker flags.
func Get() Info {
	i := Info{Version: Version, Commit: Commit, Date: Date, Go: runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			case "vcs.modified":
				i.Dirty = s.Value == "true"
			}
		}
	}
	if len(i.Commit) > 7 {
		i.Commit = i.Commit[:7]
	}
	if i.Commit == "unknown" {
		i.Commit = ""
	}
	return i
}

// String is the short form shown in the UI and by `syntaxis version`, such as
// "v1.2.3 (abc1234)" or "dev (abc1234, modified)".
func (i Info) String() string {
	var extra []string
	if i.Commit != "" {
		extra = append(extra, i.Commit)
	}
	if i.Dirty {
		extra = append(extra, "modified")
	}
	if len(extra) == 0 {
		return i.Version
	}
	return i.Version + " (" + strings.Join(extra, ", ") + ")"
}
