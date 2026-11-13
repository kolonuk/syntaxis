// SPDX-License-Identifier: GPL-2.0-or-later

package health

import (
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
)

func bp(b bool) *bool { return &b }

// healthy returns a state with no findings above Info, to mutate per test.
func healthy() *State {
	return &State{
		Providers: []ProviderState{{Name: "p", Kind: "ollama", Reachable: true, Latency: 50 * time.Millisecond, Concurrency: 2,
			Models: []ModelState{{Name: "m", Enabled: true, ContextAdvert: 32768, ContextLoaded: 32768, ContextUsed: 32768, ContextSource: "api", ToolsSupported: bp(true)}}}},
		Sandbox:      SandboxState{DockerReachable: true, RunscRegistered: true, SocketProxyUsed: true, SocketProxyReachable: true, NetworkRulesApplied: true, ImagePresent: true, DiskFreePct: 60},
		Security:     SecurityState{AdminCount: 1, AdminsWith2FA: 1, RotationDays: 30, SMTPConfigured: true, MasterKeySet: true, HTTPSDetected: true, DefaultQuotasSet: true, ScanMode: "block", EgressMode: "allowlist"},
		Housekeeping: HousekeepingState{LastBackup: time.Now(), RetentionSet: true, VAPIDSet: true, PushEnabled: true},
	}
}

func find(fs []Finding, id string) *Finding {
	for i := range fs {
		if fs[i].CheckID == id {
			return &fs[i]
		}
	}
	return nil
}

func run(s *State) []Finding { return NewEngine(DefaultThresholds()).Run(s) }

func TestHealthyHasNoFindings(t *testing.T) {
	if fs := run(healthy()); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
}

func TestContextTooSmall(t *testing.T) {
	s := healthy()
	m := &s.Providers[0].Models[0]
	m.ContextLoaded, m.ContextUsed = 4096, 4096
	f := find(run(s), CheckContextSize)
	if f == nil || f.Severity != Error || f.UserMessage == "" || f.Fix == "" || f.Detail["loaded"] != "4096" {
		t.Fatalf("%+v", f)
	}
	m.ContextLoaded, m.ContextUsed = 12000, 12000
	if f := find(run(s), CheckContextSize); f == nil || f.Severity != Warning {
		t.Fatalf("want warning, got %+v", f)
	}
	m.ContextLoaded, m.ContextUsed = 16384, 16384
	if find(run(s), CheckContextSize) != nil {
		t.Fatal("16k should not warn")
	}
}

func TestContextOverrideWinsAndUndetected(t *testing.T) {
	s := healthy()
	m := &s.Providers[0].Models[0]
	m.ContextLoaded, m.ContextUsed, m.ContextOverride = 4096, 4096, 32768
	if find(run(s), CheckContextSize) != nil {
		t.Fatal("override ignored")
	}
	m.ContextOverride, m.ContextLoaded, m.ContextAdvert, m.ContextUsed = 0, 0, 0, 0
	if f := find(run(s), CheckContextSize); f == nil || f.Severity != Warning {
		t.Fatal("undetected context should warn")
	}
}

func TestContextAdvertisedOnlyIsNotTrusted(t *testing.T) {
	s := healthy()
	m := &s.Providers[0].Models[0]
	m.ContextLoaded, m.ContextUsed = 0, 0 // only advertised known
	m.ContextAdvert = 4096
	if f := find(run(s), CheckContextSize); f == nil || f.Severity != Error {
		t.Fatalf("%+v", f)
	}
}

func TestContextAdvertVsLoadedInfo(t *testing.T) {
	h := healthy()
	h.Providers[0].Models[0].ContextAdvert = 131072
	f := find(run(h), CheckContextMismatch)
	if f == nil || f.Severity != Info {
		t.Fatalf("%+v", f)
	}
	if f.Detail["advertised"] != "131072" {
		t.Fatal("detail")
	}
}

func TestTruncationRisk(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].ContextUsed = 65536 // > loaded 32768
	f := find(run(s), CheckContextRisk)
	if f == nil || f.Severity != Error || f.UserMessage == "" {
		t.Fatalf("%+v", f)
	}
}

func TestAliasMismatch(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].AliasOf = "digest-x"
	s.Providers = append(s.Providers, ProviderState{Name: "gw", Reachable: true, Concurrency: 1,
		Models: []ModelState{{Name: "alias", Enabled: true, ContextLoaded: 16384, ContextUsed: 16384, AliasOf: "digest-x", ToolsSupported: bp(true)}}})
	f := find(run(s), CheckContextAlias)
	if f == nil || f.Severity != Warning {
		t.Fatalf("%+v", f)
	}
	s.Providers[1].Models[0].ContextLoaded, s.Providers[1].Models[0].ContextUsed = 32768, 32768
	if find(run(s), CheckContextAlias) != nil {
		t.Fatal("equal windows flagged")
	}
}

func TestDisabledModelsIgnored(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].Enabled = false
	s.Providers[0].Models[0].ContextLoaded = 1024
	if find(run(s), CheckContextSize) != nil {
		t.Fatal("disabled model checked")
	}
}

func TestToolCalling(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].ToolsSupported = bp(false)
	if f := find(run(s), CheckToolCalling); f == nil || f.UserMessage == "" {
		t.Fatal("no-tools model not flagged")
	}
	s.Providers[0].Models[0].ToolsSupported = bp(true)
	s.Providers[0].Models[0].ToolProbe = bp(false)
	if f := find(run(s), CheckToolCalling); f == nil {
		t.Fatal("failed probe not flagged")
	}
	s.Providers[0].Models[0].ToolProbe = bp(true)
	if find(run(s), CheckToolCalling) != nil {
		t.Fatal("false positive")
	}
	s.Providers[0].Models[0].ToolsSupported = nil
	s.Providers[0].Models[0].ToolProbe = nil
	if find(run(s), CheckToolCalling) != nil {
		t.Fatal("unknown support must not warn")
	}
}

func TestStreamingAndTimeouts(t *testing.T) {
	s := healthy()
	s.Providers[0].StreamBuffered = bp(true)
	s.Providers[0].IdleTimeout = 30 * time.Second
	fs := run(s)
	if find(fs, CheckStreaming) == nil || find(fs, CheckTimeouts) == nil {
		t.Fatalf("%+v", fs)
	}
	s.Providers[0].StreamBuffered = bp(false)
	s.Providers[0].IdleTimeout = 10 * time.Minute
	fs = run(s)
	if find(fs, CheckStreaming) != nil || find(fs, CheckTimeouts) != nil {
		t.Fatal("false positive")
	}
}

func TestConcurrency(t *testing.T) {
	s := healthy()
	s.Providers[0].Concurrency = 0
	if f := find(run(s), CheckConcurrency); f == nil {
		t.Fatal("unset concurrency not flagged")
	}
	s.Providers[0].Concurrency, s.Providers[0].ServerParallel = 8, 2
	if f := find(run(s), CheckConcurrency); f == nil || f.Detail["server"] != "2" {
		t.Fatalf("%+v", f)
	}
	s.Providers[0].Concurrency = 2
	if find(run(s), CheckConcurrency) != nil {
		t.Fatal("matching concurrency flagged")
	}
}

func TestResidencyRoutingNotCoResident(t *testing.T) {
	s := healthy()
	s.Routing = RoutingState{Enabled: true, Provider: "p", Models: []string{"m", "helper"}}
	s.Providers[0].LoadedSupported = true
	s.Providers[0].Loaded = []provider.LoadedModel{{Name: "m", SizeBytes: 100, VRAMBytes: 100}}
	f := find(run(s), CheckResidency)
	if f == nil || f.Severity != Error || f.Subject != "p/helper" {
		t.Fatalf("%+v", f)
	}
	s.Providers[0].Loaded = append(s.Providers[0].Loaded, provider.LoadedModel{Name: "helper", SizeBytes: 10, VRAMBytes: 10})
	if find(run(s), CheckResidency) != nil {
		t.Fatal("co-resident flagged")
	}
	s.Routing.Enabled = false
	s.Providers[0].Loaded = nil
	if find(run(s), CheckResidency) != nil {
		t.Fatal("routing off must not flag")
	}
}

func TestResidencyAdminAssertedWhenUnsupported(t *testing.T) {
	s := healthy()
	s.Routing = RoutingState{Enabled: true, Provider: "p", Models: []string{"x"}}
	s.Providers[0].LoadedSupported = false
	if find(run(s), CheckResidency) != nil {
		t.Fatal("servers that cannot report residency are admin-asserted")
	}
}

func TestReloadsAndCPUSpill(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].Reloads = 5
	s.Providers[0].Loaded = []provider.LoadedModel{{Name: "m", SizeBytes: 1000, VRAMBytes: 400}}
	fs := run(s)
	if find(fs, CheckReloads) == nil {
		t.Fatal("reload thrash")
	}
	f := find(fs, CheckCPUSpill)
	if f == nil || f.Detail["in_gpu_percent"] != "40" {
		t.Fatalf("%+v", f)
	}
}

func TestProviderStates(t *testing.T) {
	s := healthy()
	s.Providers[0].Reachable, s.Providers[0].Err = false, "connection refused"
	f := find(run(s), CheckProviderDown)
	if f == nil || f.Severity != Error || f.UserMessage == "" {
		t.Fatalf("%+v", f)
	}
	s = healthy()
	s.Providers[0].AuthFailed = true
	if f := find(run(s), CheckProviderAuth); f == nil || f.Severity != Error {
		t.Fatal("auth")
	}
	s = healthy()
	s.Providers[0].Latency = 9 * time.Second
	if find(run(s), CheckProviderSlow) == nil {
		t.Fatal("slow")
	}
	s = healthy()
	s.Providers[0].Models = nil
	if find(run(s), CheckProviderEmpty) == nil {
		t.Fatal("empty")
	}
	s = healthy()
	s.Providers[0].MissingEnabled = []string{"gone"}
	if f := find(run(s), CheckModelGone); f == nil || f.Subject != "p/gone" {
		t.Fatal("missing model")
	}
	s = healthy()
	s.Providers[0].Models[0].Digest, s.Providers[0].Models[0].BenchmarkDigest = "new", "old"
	if find(run(s), CheckModelDigest) == nil {
		t.Fatal("digest change")
	}
}

func TestSandboxChecks(t *testing.T) {
	s := healthy()
	s.Sandbox.RunscRegistered = false
	if f := find(run(s), CheckRunsc); f == nil || f.Severity != Error || f.UserMessage == "" {
		t.Fatal("runsc missing must be an error with a user banner")
	}
	s = healthy()
	s.Sandbox.DockerReachable, s.Sandbox.DockerErr = false, "no such host"
	fs := run(s)
	if find(fs, CheckDockerAccess) == nil {
		t.Fatal("docker down")
	}
	if find(fs, CheckRunsc) != nil || find(fs, CheckNetworkRules) != nil {
		t.Fatal("dependent checks must not pile on when docker is down")
	}
	s = healthy()
	s.Sandbox.RawSocketMounted = true
	if f := find(run(s), CheckSocketProxy); f == nil || f.Severity != Error {
		t.Fatal("raw socket")
	}
	s = healthy()
	s.Sandbox.SocketProxyReachable = false
	if f := find(run(s), CheckSocketProxy); f == nil || f.Severity != Error {
		t.Fatal("proxy unreachable")
	}
	s = healthy()
	s.Sandbox.SocketProxyTooOpen = true
	if f := find(run(s), CheckSocketProxy); f == nil || f.Severity != Warning {
		t.Fatal("proxy too permissive")
	}
	s = healthy()
	s.Sandbox.NetworkRulesApplied = false
	if f := find(run(s), CheckNetworkRules); f == nil || f.Severity != Error {
		t.Fatal("network rules")
	}
	s = healthy()
	s.Sandbox.ImagePresent = false
	if find(run(s), CheckSandboxImage) == nil {
		t.Fatal("image")
	}
	s = healthy()
	s.Sandbox.DiskFreePct = 3
	if f := find(run(s), CheckDisk); f == nil || f.Severity != Error {
		t.Fatal("disk full")
	}
	s.Sandbox.DiskFreePct = 10
	if f := find(run(s), CheckDisk); f == nil || f.Severity != Warning {
		t.Fatal("disk low")
	}
	s = healthy()
	s.Sandbox.SessionMemCapMB, s.Sandbox.MaxConcurrentSessions, s.Sandbox.MemFreeMB = 4096, 10, 8000
	if find(run(s), CheckMemory) == nil {
		t.Fatal("memory overcommit")
	}
}

func TestSecurityChecks(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SecurityState)
		id   string
		sev  Severity
	}{
		{"admin2fa", func(s *SecurityState) { s.AdminsWith2FA = 0 }, CheckAdmin2FA, Error},
		{"rotation", func(s *SecurityState) { s.RotationDays = 0 }, CheckRotation, Warning},
		{"smtp", func(s *SecurityState) { s.SMTPConfigured = false }, CheckSMTP, Warning},
		{"nokey", func(s *SecurityState) { s.MasterKeySet = false }, CheckMasterKey, Error},
		{"keyindata", func(s *SecurityState) { s.MasterKeyInDataDir = true }, CheckMasterKey, Warning},
		{"https", func(s *SecurityState) { s.HTTPSDetected = false }, CheckHTTPS, Warning},
		{"adminpub", func(s *SecurityState) { s.AdminListenPublic = true }, CheckAdminExposed, Error},
		{"quotas", func(s *SecurityState) { s.DefaultQuotasSet = false }, CheckDefaultQuotas, Warning},
		{"scanner", func(s *SecurityState) { s.ScanMode = "off" }, CheckScannerOff, Warning},
		{"egress", func(s *SecurityState) { s.EgressMode = "open" }, CheckEgressOpen, Info},
		{"proxyempty", func(s *SecurityState) { s.ProxyAuthEnabled, s.ProxyTrustedEmpty = true, true }, CheckProxyAuth, Error},
		{"proxyall", func(s *SecurityState) { s.ProxyAuthEnabled, s.ProxyTrustsAll = true, true }, CheckProxyAuth, Error},
	}
	for _, c := range cases {
		s := healthy()
		c.mut(&s.Security)
		f := find(run(s), c.id)
		if f == nil || f.Severity != c.sev || f.Fix == "" || f.Message == "" {
			t.Errorf("%s: %+v", c.name, f)
		}
	}
	// proxy auth off with empty list is fine
	s := healthy()
	s.Security.ProxyTrustedEmpty = true
	if find(run(s), CheckProxyAuth) != nil {
		t.Fatal("proxy check must only apply when enabled")
	}
}

func TestHousekeeping(t *testing.T) {
	s := healthy()
	s.Housekeeping.LastBackup = time.Time{}
	if find(run(s), CheckBackups) == nil {
		t.Fatal("never backed up")
	}
	s.Housekeeping.LastBackup = time.Now().Add(-30 * 24 * time.Hour)
	if find(run(s), CheckBackups) == nil {
		t.Fatal("stale backup")
	}
	s = healthy()
	s.Housekeeping.RetentionSet = false
	s.Housekeeping.LogDirBytes = 5 << 30
	s.Housekeeping.ClockChecked, s.Housekeeping.ClockSkew = true, 2*time.Minute
	s.Housekeeping.VAPIDSet = false
	fs := run(s)
	for _, id := range []string{CheckRetention, CheckLogGrowth, CheckClockSkew, CheckVAPID} {
		if find(fs, id) == nil {
			t.Errorf("missing %s", id)
		}
	}
	if f := find(fs, CheckClockSkew); f.Severity != Error {
		t.Fatal("clock skew breaks TOTP: error")
	}
	s.Housekeeping.ClockChecked = false
	if find(run(s), CheckClockSkew) != nil {
		t.Fatal("unchecked clock flagged")
	}
}

func TestSnoozeNeverHidesErrors(t *testing.T) {
	e := NewEngine(DefaultThresholds())
	s := healthy()
	s.Security.SMTPConfigured = false
	s.Sandbox.RunscRegistered = false
	fs := e.Run(s)
	smtp, runsc := *find(fs, CheckSMTP), *find(fs, CheckRunsc)
	if !e.Snooze(smtp, time.Now().Add(time.Hour)) {
		t.Fatal("warning must be snoozable")
	}
	if e.Snooze(runsc, time.Now().Add(time.Hour)) {
		t.Fatal("errors must not be snoozable")
	}
	fs = e.Run(s)
	if !find(fs, CheckSMTP).Snoozed || find(fs, CheckRunsc).Snoozed {
		t.Fatal("snooze state wrong")
	}
	e.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if find(e.Run(s), CheckSMTP).Snoozed {
		t.Fatal("snooze did not expire")
	}
}

func TestSortAndSummary(t *testing.T) {
	s := healthy()
	s.Security.EgressMode = "open"
	s.Security.SMTPConfigured = false
	s.Sandbox.RunscRegistered = false
	fs := run(s)
	if fs[0].Severity != Error || fs[len(fs)-1].Severity != Info {
		t.Fatalf("order: %v ... %v", fs[0].Severity, fs[len(fs)-1].Severity)
	}
	sum := Summarise(fs)
	if sum.Errors != 1 || sum.Warnings != 1 || sum.Infos < 1 || sum.Worst != Error {
		t.Fatalf("%+v", sum)
	}
	if Summarise(nil).Worst != "" {
		t.Fatal("empty summary")
	}
}

func TestUserBanners(t *testing.T) {
	s := healthy()
	s.Providers[0].Models[0].ContextLoaded, s.Providers[0].Models[0].ContextUsed = 4096, 4096
	s.Security.SMTPConfigured = false // admin-only
	fs := run(s)
	b := UserBanners(fs, "p/m")
	if len(b) != 1 {
		t.Fatalf("%v", b)
	}
	// unrelated user (different model) sees nothing about this model
	if len(UserBanners(fs, "other/model")) != 0 {
		t.Fatal("banner leaked to unaffected users")
	}
	// banners are non-technical (no token counts / config names)
	for _, m := range b {
		if len(m) > 160 {
			t.Fatalf("banner too long: %q", m)
		}
	}
}

func TestCPUOnlyModelIsNotASpill(t *testing.T) {
	s := healthy()
	s.Providers[0].Loaded = []provider.LoadedModel{{Name: "cpu-model", SizeBytes: 1000, VRAMBytes: 0}}
	if find(run(s), CheckCPUSpill) != nil {
		t.Fatal("a model with no GPU memory at all is CPU-only by design")
	}
}
