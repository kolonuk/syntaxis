// SPDX-License-Identifier: GPL-2.0-or-later

// Package health is the configuration health-check engine behind
// `syntaxis doctor`, the admin Health page and user-facing banners. Checks are
// pure functions over a State snapshot, so each one is unit-testable with
// fake provider data.
package health

import (
	"sort"
	"sync"
	"time"
)

// Severity of a finding.
type Severity string

// Severities.
const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

func (s Severity) rank() int {
	switch s {
	case Error:
		return 3
	case Warning:
		return 2
	}
	return 1
}

// Check ids (stable: docs/pitfalls.md links to these).
const (
	CheckContextSize     = "context.size"
	CheckContextMismatch = "context.mismatch"
	CheckContextAlias    = "context.alias-mismatch"
	CheckContextRisk     = "context.truncation-risk"
	CheckToolCalling     = "tools.support"
	CheckStreaming       = "streaming.buffering"
	CheckTimeouts        = "streaming.timeouts"
	CheckConcurrency     = "queue.concurrency"
	CheckResidency       = "routing.residency"
	CheckReloads         = "routing.reloads"
	CheckCPUSpill        = "routing.cpu-spill"
	CheckProviderDown    = "provider.unreachable"
	CheckProviderSlow    = "provider.slow"
	CheckProviderAuth    = "provider.auth"
	CheckProviderEmpty   = "provider.no-models"
	CheckModelGone       = "provider.model-missing"
	CheckModelDigest     = "provider.model-changed"
	CheckRunsc           = "sandbox.runsc"
	CheckDockerAccess    = "sandbox.docker"
	CheckSocketProxy     = "sandbox.socket-proxy"
	CheckNetworkRules    = "sandbox.network-rules"
	CheckSandboxImage    = "sandbox.image"
	CheckDisk            = "sandbox.disk"
	CheckMemory          = "sandbox.memory"
	CheckAdmin2FA        = "security.admin-2fa"
	CheckRotation        = "security.rotation-off"
	CheckSMTP            = "security.smtp"
	CheckMasterKey       = "security.master-key"
	CheckHTTPS           = "security.https"
	CheckAdminExposed    = "security.admin-exposed"
	CheckDefaultQuotas   = "security.default-quotas"
	CheckScannerOff      = "security.scanner-off"
	CheckEgressOpen      = "security.egress-open"
	CheckProxyAuth       = "security.proxy-auth"
	CheckBackups         = "housekeeping.backups"
	CheckRetention       = "housekeeping.retention"
	CheckLogGrowth       = "housekeeping.log-growth"
	CheckClockSkew       = "housekeeping.clock-skew"
	CheckVAPID           = "housekeeping.vapid"
)

// Finding is one problem or note.
type Finding struct {
	CheckID     string            `json:"check_id"`
	Severity    Severity          `json:"severity"`
	Subject     string            `json:"subject"`                // provider/model/etc, "" for global
	Message     string            `json:"message"`                // plain-language explanation (admin)
	Fix         string            `json:"fix"`                    // suggested fix
	UserMessage string            `json:"user_message,omitempty"` // short, non-technical banner; "" = admin only
	Detail      map[string]string `json:"detail,omitempty"`
	Snoozed     bool              `json:"snoozed,omitempty"`
}

// Key identifies a finding for snoozing.
func (f Finding) Key() string { return f.CheckID + "|" + f.Subject }

// Thresholds are admin-tunable.
type Thresholds struct {
	MinContextWarn   int           // default 16384
	MinContextError  int           // default 8192
	SlowProvider     time.Duration // default 5s ping
	MinStreamTimeout time.Duration // default 120s
	MaxClockSkew     time.Duration // default 30s (breaks TOTP)
	BackupStale      time.Duration // default 7d
	DiskWarnPct      float64       // free percent below which to warn, default 15
	LogWarnBytes     int64         // default 1 GiB
	ReloadThreshold  int           // reloads per window that count as thrashing, default 3
}

// DefaultThresholds returns sane defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{MinContextWarn: 16384, MinContextError: 8192, SlowProvider: 5 * time.Second,
		MinStreamTimeout: 120 * time.Second, MaxClockSkew: 30 * time.Second, BackupStale: 7 * 24 * time.Hour,
		DiskWarnPct: 15, LogWarnBytes: 1 << 30, ReloadThreshold: 3}
}

// Engine runs checks and tracks snoozes.
type Engine struct {
	T      Thresholds
	Now    func() time.Time
	checks []func(*State, Thresholds) []Finding

	mu      sync.Mutex
	snoozed map[string]time.Time
}

// NewEngine returns an engine with all built-in checks.
func NewEngine(t Thresholds) *Engine {
	return &Engine{T: t, Now: time.Now, snoozed: map[string]time.Time{}, checks: allChecks()}
}

// Run evaluates every check, applies snoozes, and sorts by severity.
func (e *Engine) Run(s *State) []Finding {
	var out []Finding
	for _, c := range e.checks {
		out = append(out, c(s, e.T)...)
	}
	e.mu.Lock()
	for i := range out {
		if until, ok := e.snoozed[out[i].Key()]; ok && e.Now().Before(until) && out[i].Severity != Error {
			out[i].Snoozed = true
		}
	}
	e.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity.rank() != out[j].Severity.rank() {
			return out[i].Severity.rank() > out[j].Severity.rank()
		}
		return out[i].Key() < out[j].Key()
	})
	return out
}

// Snooze hides a non-error finding until the given time. Errors cannot be snoozed.
func (e *Engine) Snooze(f Finding, until time.Time) bool {
	if f.Severity == Error {
		return false
	}
	e.mu.Lock()
	e.snoozed[f.Key()] = until
	e.mu.Unlock()
	return true
}

// Summary is the admin header badge.
type Summary struct {
	Errors   int      `json:"errors"`
	Warnings int      `json:"warnings"`
	Infos    int      `json:"infos"`
	Worst    Severity `json:"worst"`
}

// Summarise counts unsnoozed findings.
func Summarise(fs []Finding) Summary {
	var s Summary
	for _, f := range fs {
		if f.Snoozed {
			continue
		}
		switch f.Severity {
		case Error:
			s.Errors++
		case Warning:
			s.Warnings++
		default:
			s.Infos++
		}
	}
	switch {
	case s.Errors > 0:
		s.Worst = Error
	case s.Warnings > 0:
		s.Worst = Warning
	case s.Infos > 0:
		s.Worst = Info
	}
	return s
}

// UserBanners returns short plain messages relevant to a user's session
// (model on the given provider). Admin-only findings (empty UserMessage) are omitted.
func UserBanners(fs []Finding, subjects ...string) []string {
	want := map[string]bool{"": true}
	for _, s := range subjects {
		want[s] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if f.Snoozed || f.UserMessage == "" || f.Severity == Info || !want[f.Subject] {
			continue
		}
		if !seen[f.UserMessage] {
			seen[f.UserMessage] = true
			out = append(out, f.UserMessage)
		}
	}
	return out
}
