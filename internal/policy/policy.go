// SPDX-License-Identifier: GPL-2.0-or-later

// Package policy combines the settings that can come from a user's level, their group and the
// user themselves into the one effective set the rest of the server uses.
//
// Order, from least to most specific: instance defaults, the user's level, their group, the
// user's own settings.
//
//   - Numbers and modes (quotas, session limit, egress mode, allowed models): the most specific
//     layer that sets a value wins, so an explicit setting on a user overrides their group.
//   - On/off permissions (terminal, cloud tooling): a permission is off if any layer turns it
//     off. A user cannot be given back something their level or group takes away; to allow it,
//     change the level or group.
package policy

import "github.com/kolonuk/syntaxis/internal/store"

// Apply returns u with its level's and group's settings filled in wherever u has none of its
// own. Either layer may be nil.
func Apply(u store.User, level, group *store.PolicyData) store.User {
	for _, layer := range []*store.PolicyData{group, level} { // most specific first: fill only what is still unset
		if layer == nil {
			continue
		}
		if u.QuotaDailyTokens == 0 {
			u.QuotaDailyTokens = layer.DailyTokens
		}
		if u.QuotaMonthlyTokens == 0 {
			u.QuotaMonthlyTokens = layer.MonthlyTokens
		}
		if u.MaxSessions == 0 {
			u.MaxSessions = layer.MaxSessions
		}
		if u.QuotaCPUMinutes == 0 {
			u.QuotaCPUMinutes = layer.CPUMinutes
		}
		if u.EgressMode == "" {
			u.EgressMode = layer.EgressMode
		}
		if len(u.AllowedModels) == 0 {
			u.AllowedModels = layer.AllowedModels
		}
		u.TerminalDisabled = u.TerminalDisabled || layer.NoTerminal
		u.CloudToolsDisabled = u.CloudToolsDisabled || layer.NoCloudTools
	}
	return u
}

// Valid reports whether the settings are acceptable (used before an admin saves them).
func Valid(p store.PolicyData) string {
	switch p.EgressMode {
	case "", "open", "allowlist", "none":
	default:
		return "egress mode must be open, allowlist or none"
	}
	if p.DailyTokens < -1 || p.MonthlyTokens < -1 || p.MaxSessions < -1 || p.CPUMinutes < -1 {
		return "limits cannot be below -1 (-1 means unlimited)"
	}
	return ""
}
