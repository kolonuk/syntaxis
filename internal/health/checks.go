// SPDX-License-Identifier: GPL-2.0-or-later

package health

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func allChecks() []func(*State, Thresholds) []Finding {
	return []func(*State, Thresholds) []Finding{
		checkContext, checkTools, checkStreaming, checkConcurrency, checkResidency,
		checkProvider, checkSandbox, checkSecurity, checkHousekeeping,
	}
}

func d(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func subject(p ProviderState, m ModelState) string { return p.Name + "/" + m.Name }

// effective returns the context window Syntaxis should trust and where it came from.
func effective(m ModelState) (int, string) {
	switch {
	case m.ContextOverride > 0:
		return m.ContextOverride, "admin override"
	case m.ContextLoaded > 0:
		return m.ContextLoaded, m.ContextSource
	case m.ContextAdvert > 0:
		return m.ContextAdvert, "model's advertised maximum (the server may run with less)"
	}
	return 0, ""
}

func checkContext(s *State, t Thresholds) []Finding {
	var out []Finding
	byAlias := map[string][]struct {
		sub string
		ctx int
	}{}
	for _, p := range s.Providers {
		for _, m := range p.Models {
			if !m.Enabled {
				continue
			}
			sub := subject(p, m)
			eff, src := effective(m)
			det := d("advertised", fmt.Sprint(m.ContextAdvert), "loaded", fmt.Sprint(m.ContextLoaded), "used_by_syntaxis", fmt.Sprint(m.ContextUsed), "effective", fmt.Sprint(eff), "source", src)
			if eff == 0 {
				out = append(out, Finding{CheckID: CheckContextSize, Severity: Warning, Subject: sub, Detail: det,
					Message: "Syntaxis could not detect this model's context window.",
					Fix:     "Set an explicit context size override for this model in the admin Models page."})
			} else if eff < t.MinContextError {
				out = append(out, Finding{CheckID: CheckContextSize, Severity: Error, Subject: sub, Detail: det,
					Message:     fmt.Sprintf("The effective context window is %d tokens (source: %s), too small for agent work (minimum %d).", eff, src, t.MinContextError),
					Fix:         "Increase the context window on the LLM server (see docs/pitfalls.md) or choose another model.",
					UserMessage: "The selected model has a very small memory window; agent tasks will fail or lose track. Ask your admin or pick another model."})
			} else if eff < t.MinContextWarn {
				out = append(out, Finding{CheckID: CheckContextSize, Severity: Warning, Subject: sub, Detail: det,
					Message:     fmt.Sprintf("The effective context window is %d tokens (source: %s); agent work works best with at least %d.", eff, src, t.MinContextWarn),
					Fix:         "Increase the context window on the LLM server, accepting the extra KV-cache memory (see docs/pitfalls.md).",
					UserMessage: "The selected model has a small memory window; long tasks may lose context."})
			}
			// advertised vs loaded
			if m.ContextAdvert > 0 && m.ContextLoaded > 0 && m.ContextLoaded < m.ContextAdvert {
				out = append(out, Finding{CheckID: CheckContextMismatch, Severity: Info, Subject: sub, Detail: det,
					Message: fmt.Sprintf("The model advertises %d tokens but the server runs it with %d. Syntaxis uses %d.", m.ContextAdvert, m.ContextLoaded, m.ContextLoaded),
					Fix:     "Usually intended (memory trade-off). Raise the server's context setting if you need more."})
			}
			// truncation risk: Syntaxis budget above the real window
			if m.ContextUsed > 0 && m.ContextLoaded > 0 && m.ContextUsed > m.ContextLoaded {
				out = append(out, Finding{CheckID: CheckContextRisk, Severity: Error, Subject: sub, Detail: det,
					Message:     fmt.Sprintf("Syntaxis is configured to use %d tokens but the server's real window is %d. The server will silently truncate input.", m.ContextUsed, m.ContextLoaded),
					Fix:         "Lower the model's context setting in Syntaxis (or remove the override) so it is at or below the server's value, or raise the server's setting.",
					UserMessage: "The model may silently forget parts of the conversation. Your admin has been notified."})
			}
			if m.AliasOf != "" && eff > 0 {
				byAlias[m.AliasOf] = append(byAlias[m.AliasOf], struct {
					sub string
					ctx int
				}{sub, eff})
			}
		}
	}
	keys := make([]string, 0, len(byAlias))
	for k := range byAlias {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := byAlias[k]
		same := true
		for _, e := range g {
			if e.ctx != g[0].ctx {
				same = false
			}
		}
		if len(g) > 1 && !same {
			var parts []string
			for _, e := range g {
				parts = append(parts, fmt.Sprintf("%s=%d", e.sub, e.ctx))
			}
			out = append(out, Finding{CheckID: CheckContextAlias, Severity: Warning, Subject: g[0].sub, Detail: d("group", k, "values", strings.Join(parts, ", ")),
				Message: "The same model is served under different names with different context windows: " + strings.Join(parts, ", ") + ". A gateway alias may point at a model loaded with a smaller window.",
				Fix:     "Align the context settings, or only enable the alias with the correct window."})
		}
	}
	return out
}

func checkTools(s *State, _ Thresholds) []Finding {
	var out []Finding
	for _, p := range s.Providers {
		for _, m := range p.Models {
			if !m.Enabled {
				continue
			}
			sub := subject(p, m)
			noSupport := m.ToolsSupported != nil && !*m.ToolsSupported
			probeFail := m.ToolProbe != nil && !*m.ToolProbe
			if noSupport || probeFail {
				why := "reports no tool-calling support"
				if probeFail && !noSupport {
					why = "failed a quick tool-call probe"
				}
				out = append(out, Finding{CheckID: CheckToolCalling, Severity: Warning, Subject: sub,
					Message:     "This model " + why + ", so agent mode is disabled or flagged for it. Chat-only mode still works.",
					Fix:         "Use a model with tool-calling support, or leave it chat-only. Run the benchmark to compare models.",
					UserMessage: "This model can chat but cannot run tools, so it can't act as a coding agent. Pick a different model for agent work."})
			}
		}
	}
	return out
}

func checkStreaming(s *State, t Thresholds) []Finding {
	var out []Finding
	for _, p := range s.Providers {
		if p.StreamBuffered != nil && *p.StreamBuffered {
			out = append(out, Finding{CheckID: CheckStreaming, Severity: Warning, Subject: p.Name,
				Message:     "Responses from this provider arrive all at once, so a gateway or reverse proxy in front of it is buffering the stream.",
				Fix:         "Disable response buffering for the LLM route (e.g. nginx: proxy_buffering off; add X-Accel-Buffering: no).",
				UserMessage: "Replies may appear all at once instead of streaming."})
		}
		if p.IdleTimeout > 0 && p.IdleTimeout < t.MinStreamTimeout {
			out = append(out, Finding{CheckID: CheckTimeouts, Severity: Warning, Subject: p.Name, Detail: d("timeout", p.IdleTimeout.String(), "recommended", t.MinStreamTimeout.String()),
				Message:     fmt.Sprintf("A timeout of %s sits in front of this provider; long generations will be cut off (recommended at least %s).", p.IdleTimeout, t.MinStreamTimeout),
				Fix:         "Raise the proxy/gateway read and idle timeouts for the LLM route.",
				UserMessage: "Long answers may be cut off."})
		}
	}
	return out
}

func checkConcurrency(s *State, _ Thresholds) []Finding {
	var out []Finding
	for _, p := range s.Providers {
		switch {
		case p.Concurrency <= 0:
			out = append(out, Finding{CheckID: CheckConcurrency, Severity: Warning, Subject: p.Name,
				Message: "No concurrency limit is configured for this provider, so Syntaxis cannot queue fairly.",
				Fix:     "Set the provider's concurrency to match the server (e.g. Ollama's OLLAMA_NUM_PARALLEL)."})
		case p.ServerParallel > 0 && p.Concurrency > p.ServerParallel:
			out = append(out, Finding{CheckID: CheckConcurrency, Severity: Warning, Subject: p.Name, Detail: d("configured", fmt.Sprint(p.Concurrency), "server", fmt.Sprint(p.ServerParallel)),
				Message:     fmt.Sprintf("Concurrency is %d but the server serves %d in parallel; requests will queue inside the server and time out unpredictably.", p.Concurrency, p.ServerParallel),
				Fix:         "Lower Syntaxis's concurrency for this provider to the server's real parallelism.",
				UserMessage: "The model server is busy; replies may be slower than usual."})
		}
	}
	return out
}

func checkResidency(s *State, t Thresholds) []Finding {
	var out []Finding
	for _, p := range s.Providers {
		for _, m := range p.Models {
			if m.Enabled && m.Reloads >= t.ReloadThreshold {
				out = append(out, Finding{CheckID: CheckReloads, Severity: Warning, Subject: subject(p, m), Detail: d("reloads", fmt.Sprint(m.Reloads)),
					Message:     fmt.Sprintf("This model was reloaded %d times recently, which causes long latency spikes (another model is evicting it).", m.Reloads),
					Fix:         "Keep fewer models in use, raise the server's keep-alive, or add memory.",
					UserMessage: "The model is being reloaded repeatedly; replies may be slow."})
			}
		}
		for _, l := range p.Loaded {
			if l.SizeBytes > 0 && l.VRAMBytes < l.SizeBytes {
				pct := int(100 * float64(l.VRAMBytes) / float64(l.SizeBytes))
				out = append(out, Finding{CheckID: CheckCPUSpill, Severity: Warning, Subject: p.Name + "/" + l.Name, Detail: d("in_gpu_percent", fmt.Sprint(pct)),
					Message:     fmt.Sprintf("Only %d%% of this loaded model is in GPU memory; the rest runs from system RAM on the CPU, which is much slower.", pct),
					Fix:         "Use a smaller model/quantisation or a smaller context window, or free GPU memory.",
					UserMessage: "The model is running partly on the CPU, so replies will be slow."})
			}
		}
	}
	if s.Routing.Enabled {
		for _, p := range s.Providers {
			if p.Name != s.Routing.Provider {
				continue
			}
			if !p.LoadedSupported {
				continue // residency is admin-asserted for servers that cannot report it
			}
			loaded := map[string]bool{}
			for _, l := range p.Loaded {
				loaded[l.Name] = true
			}
			for _, m := range s.Routing.Models {
				if !loaded[m] {
					out = append(out, Finding{CheckID: CheckResidency, Severity: Error, Subject: p.Name + "/" + m,
						Message: "The routing profile includes a model that is not currently resident. Routing would force a model swap mid-session, so it is refused/disabled and the single session model is used instead.",
						Fix:     "Load all routing models together (check memory fits), or disable routing."})
				}
			}
		}
	}
	return out
}

func checkProvider(s *State, t Thresholds) []Finding {
	var out []Finding
	for _, p := range s.Providers {
		switch {
		case p.AuthFailed:
			out = append(out, Finding{CheckID: CheckProviderAuth, Severity: Error, Subject: p.Name,
				Message: "The provider rejected Syntaxis's credentials.", Fix: "Update the provider's API key in the admin Providers page.",
				UserMessage: "The model service is not accepting requests right now."})
			continue
		case !p.Reachable:
			out = append(out, Finding{CheckID: CheckProviderDown, Severity: Error, Subject: p.Name, Detail: d("error", p.Err),
				Message: "The provider is unreachable: " + p.Err, Fix: "Check the base URL, that the LLM server is running, and the network between Syntaxis and it.",
				UserMessage: "The model service is unavailable right now. Please try again later."})
			continue
		}
		if p.Latency > t.SlowProvider {
			out = append(out, Finding{CheckID: CheckProviderSlow, Severity: Warning, Subject: p.Name, Detail: d("latency", p.Latency.String()),
				Message: fmt.Sprintf("The provider took %s to answer a simple request.", p.Latency.Round(time.Millisecond)), Fix: "Check server load and the network path.",
				UserMessage: "The model service is responding slowly."})
		}
		if len(p.Models) == 0 {
			out = append(out, Finding{CheckID: CheckProviderEmpty, Severity: Warning, Subject: p.Name,
				Message: "The provider reports no models.", Fix: "Pull or configure a model on the LLM server (Syntaxis never pulls models itself)."})
		}
		for _, name := range p.MissingEnabled {
			out = append(out, Finding{CheckID: CheckModelGone, Severity: Warning, Subject: p.Name + "/" + name,
				Message: "An enabled model is no longer listed by the provider.", Fix: "Restore the model on the server or disable it in Syntaxis.",
				UserMessage: "A model you were using is no longer available."})
		}
		for _, m := range p.Models {
			if m.Enabled && m.BenchmarkDigest != "" && m.Digest != "" && m.Digest != m.BenchmarkDigest {
				out = append(out, Finding{CheckID: CheckModelDigest, Severity: Info, Subject: subject(p, m),
					Message: "This model's digest changed since its last benchmark (it was updated or replaced).", Fix: "Re-run the benchmark."})
			}
		}
	}
	return out
}

func checkSandbox(s *State, _ Thresholds) []Finding {
	var out []Finding
	sb := s.Sandbox
	user := "Coding sandboxes are unavailable right now. Please try again later."
	if !sb.DockerReachable {
		out = append(out, Finding{CheckID: CheckDockerAccess, Severity: Error, Message: "Syntaxis cannot reach Docker: " + sb.DockerErr,
			Fix: "Check SYNTAXIS_DOCKER_HOST and that the docker-socket-proxy service is running.", UserMessage: user})
	} else if !sb.RunscRegistered {
		out = append(out, Finding{CheckID: CheckRunsc, Severity: Error, Message: "The gVisor runtime (runsc) is not registered with Docker, so sessions cannot be isolated. Syntaxis refuses to fall back to plain runc.",
			Fix: "Install gVisor and register it: see docs/getting-started.md (runsc install --runtime runsc, restart Docker).", UserMessage: user})
	}
	if sb.RawSocketMounted {
		out = append(out, Finding{CheckID: CheckSocketProxy, Severity: Error, Message: "The raw Docker socket is mounted into the Syntaxis container. A compromise of Syntaxis would be a compromise of the host.",
			Fix: "Use the docker-socket-proxy service from docker-compose.yml."})
	} else if sb.SocketProxyUsed && !sb.SocketProxyReachable {
		out = append(out, Finding{CheckID: CheckSocketProxy, Severity: Error, Message: "The Docker socket proxy is configured but unreachable.", Fix: "Start the docker-socket-proxy service and check SYNTAXIS_DOCKER_HOST.", UserMessage: user})
	} else if sb.SocketProxyTooOpen {
		out = append(out, Finding{CheckID: CheckSocketProxy, Severity: Warning, Message: "The Docker socket proxy permits more API calls than Syntaxis needs (e.g. exec on arbitrary containers, privileged creation).",
			Fix: "Restrict the proxy to containers, volumes, networks and images only (see docker-compose.yml)."})
	}
	if sb.DockerReachable && !sb.NetworkRulesApplied {
		out = append(out, Finding{CheckID: CheckNetworkRules, Severity: Error, Message: "Egress firewall rules are not applied to the sandbox network, so sandboxes might reach the host, LAN or metadata endpoints.",
			Fix: "Run `syntaxis doctor --fix-network` or apply the rules documented in docs/security.md; sandboxes will not start until this passes."})
	}
	if sb.DockerReachable && !sb.ImagePresent {
		out = append(out, Finding{CheckID: CheckSandboxImage, Severity: Error, Message: "The sandbox image is not present on the Docker host.",
			Fix: "Build or pull the image configured in SYNTAXIS_SANDBOX_IMAGE.", UserMessage: user})
	}
	if sb.DiskFreePct > 0 && sb.DiskFreePct < 5 {
		out = append(out, Finding{CheckID: CheckDisk, Severity: Error, Message: fmt.Sprintf("Only %.0f%% of disk is free.", sb.DiskFreePct), Fix: "Run cleanup now in the admin Disk page; lower retention.",
			UserMessage: "The server is almost out of disk space; new work may fail."})
	} else if sb.DiskFreePct > 0 && sb.DiskFreePct < 15 {
		out = append(out, Finding{CheckID: CheckDisk, Severity: Warning, Message: fmt.Sprintf("Only %.0f%% of disk is free.", sb.DiskFreePct), Fix: "Run cleanup now or shorten retention."})
	}
	if sb.SessionMemCapMB > 0 && sb.MaxConcurrentSessions > 0 && sb.MemFreeMB > 0 && sb.SessionMemCapMB*sb.MaxConcurrentSessions > sb.MemFreeMB {
		out = append(out, Finding{CheckID: CheckMemory, Severity: Warning, Detail: d("cap_mb", fmt.Sprint(sb.SessionMemCapMB), "sessions", fmt.Sprint(sb.MaxConcurrentSessions), "free_mb", fmt.Sprint(sb.MemFreeMB)),
			Message: "The per-session memory cap times the allowed concurrent sessions exceeds free host memory; the host may run out of memory under load.", Fix: "Lower the session memory cap or the concurrent session limit."})
	}
	return out
}

func checkSecurity(s *State, _ Thresholds) []Finding {
	var out []Finding
	sc := s.Security
	if sc.AdminCount > 0 && sc.AdminsWith2FA < sc.AdminCount {
		out = append(out, Finding{CheckID: CheckAdmin2FA, Severity: Error, Detail: d("without_2fa", fmt.Sprint(sc.AdminCount-sc.AdminsWith2FA)),
			Message: "Some admin accounts have not enabled 2FA, which is mandatory.", Fix: "Those admins must complete 2FA setup at next login."})
	}
	if sc.RotationDays == 0 {
		out = append(out, Finding{CheckID: CheckRotation, Severity: Warning, Message: "Password rotation for users without 2FA is switched off.", Fix: "Set a rotation interval (default 30 days) or require 2FA."})
	}
	if !sc.SMTPConfigured {
		out = append(out, Finding{CheckID: CheckSMTP, Severity: Warning, Message: "SMTP is not configured: invite and reset emails are only written to the log.", Fix: "Set SYNTAXIS_SMTP_HOST and friends (see docs/configuration.md)."})
	}
	if !sc.MasterKeySet {
		out = append(out, Finding{CheckID: CheckMasterKey, Severity: Error, Message: "No master key is set, so the secrets vault and 2FA secrets cannot be encrypted.", Fix: "Generate one with `syntaxis keygen` and set SYNTAXIS_MASTER_KEY or SYNTAXIS_MASTER_KEY_FILE."})
	} else if sc.MasterKeyInDataDir {
		out = append(out, Finding{CheckID: CheckMasterKey, Severity: Warning, Message: "The master key file lives inside the data directory, so one backup or leak exposes both the data and the key.", Fix: "Store the key outside the data directory."})
	}
	if !sc.HTTPSDetected {
		out = append(out, Finding{CheckID: CheckHTTPS, Severity: Warning, Message: "HTTPS was not detected (no TLS/X-Forwarded-Proto=https). Secure cookies and the PWA need HTTPS.", Fix: "Put Syntaxis behind a TLS-terminating reverse proxy."})
	}
	if sc.AdminListenPublic {
		out = append(out, Finding{CheckID: CheckAdminExposed, Severity: Error, Message: "The admin listener is bound to a public address.", Fix: "Bind SYNTAXIS_ADMIN_LISTEN to loopback, a LAN or a VPN address."})
	}
	if !sc.DefaultQuotasSet {
		out = append(out, Finding{CheckID: CheckDefaultQuotas, Severity: Warning, Message: "No default token quota is set, so one user can consume unlimited GPU time.", Fix: "Set default daily/monthly token quotas in Policies."})
	}
	if sc.ScanMode == "off" {
		out = append(out, Finding{CheckID: CheckScannerOff, Severity: Warning, Message: "The secret scanner is off.", Fix: "Set the scanner to warn or block."})
	}
	if sc.EgressMode == "open" {
		out = append(out, Finding{CheckID: CheckEgressOpen, Severity: Info, Message: "Sandbox egress is open (full internet, minus host/LAN/metadata/SMTP). Fine for trusted users.", Fix: "Use allowlist or none for stricter instances."})
	}
	if sc.ProxyAuthEnabled && (sc.ProxyTrustedEmpty || sc.ProxyTrustsAll) {
		out = append(out, Finding{CheckID: CheckProxyAuth, Severity: Error, Message: "Trusted-proxy header auth is on but the trusted proxy list is empty or trusts everything; anyone could forge the identity header.", Fix: "List only your reverse proxy's address(es), or turn the mode off."})
	}
	return out
}

func checkHousekeeping(s *State, t Thresholds) []Finding {
	var out []Finding
	h := s.Housekeeping
	now := time.Now()
	if h.LastBackup.IsZero() {
		out = append(out, Finding{CheckID: CheckBackups, Severity: Warning, Message: "No backup has ever been run.", Fix: "Schedule `syntaxis backup` (DB + workspaces) and keep the master key separately."})
	} else if now.Sub(h.LastBackup) > t.BackupStale {
		out = append(out, Finding{CheckID: CheckBackups, Severity: Warning, Message: fmt.Sprintf("The last backup was %s ago.", now.Sub(h.LastBackup).Round(time.Hour)), Fix: "Check the backup schedule."})
	}
	if !h.RetentionSet {
		out = append(out, Finding{CheckID: CheckRetention, Severity: Warning, Message: "Retention policies are unset: sandboxes, workspaces and logs will grow without limit.", Fix: "Set idle/destroy/workspace/log retention in Policies."})
	}
	if t.LogWarnBytes > 0 && h.LogDirBytes > t.LogWarnBytes {
		out = append(out, Finding{CheckID: CheckLogGrowth, Severity: Warning, Message: "Logs are using a lot of disk.", Fix: "Lower log retention or enable rotation."})
	}
	if h.ClockChecked && h.ClockSkew > t.MaxClockSkew {
		out = append(out, Finding{CheckID: CheckClockSkew, Severity: Error, Message: fmt.Sprintf("The server clock is off by about %s; TOTP codes will fail and tokens may expire early.", h.ClockSkew.Round(time.Second)),
			Fix: "Enable NTP on the host.", UserMessage: "Two-factor codes may be rejected right now."})
	}
	if h.PushEnabled && !h.VAPIDSet {
		out = append(out, Finding{CheckID: CheckVAPID, Severity: Warning, Message: "Web Push is enabled but no VAPID keys are set.", Fix: "Generate keys with `syntaxis keygen --vapid`."})
	} else if !h.PushEnabled && !h.VAPIDSet {
		out = append(out, Finding{CheckID: CheckVAPID, Severity: Info, Message: "No VAPID keys are set, so push notifications are unavailable.", Fix: "Generate keys with `syntaxis keygen --vapid`."})
	}
	return out
}
