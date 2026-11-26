// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kolonuk/syntaxis/internal/buildinfo"
	"github.com/kolonuk/syntaxis/internal/uploadcheck"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/audit"
	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/bench"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/queue"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/settings"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Setting keys editable by admins.
const (
	SettingRotationDays  = "password_rotation_days"
	SettingScanMode      = "secret_scan_mode"
	SettingUserOverride  = "secret_scan_allow_user_override"
	SettingDailyTokens   = "default_daily_tokens"
	SettingMonthlyTokens = "default_monthly_tokens"
	// SettingVisionModels lists models (provider-reported names, comma separated) that accept pictures
	// although their server does not say so (OpenAI-compatible servers never do).
	SettingVisionModels = "vision_models"
	// SettingSearchURL is the address of a SearXNG instance the web_search tool uses; empty = no web search.
	SettingSearchURL = "search_url"
	// File uploads (the Files tab and message attachments) are off until an admin turns them on.
	SettingUploadsEnabled = "uploads_enabled"
	SettingUploadMaxMB    = "upload_max_mb"
	SettingUploadKinds    = "upload_kinds"
)

// AdminHandler returns the admin app: separate routes, cookie and credentials.
func (a *App) AdminHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.secureHeaders, a.logRequests)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": buildinfo.Get()})
	})
	r.Get("/api/version", handleVersion)
	r.Get("/internal/firewall", a.handleFirewallState)
	r.Post("/internal/firewall/log", a.handleFirewallLog)
	r.Get("/internal/update", a.handleUpdatePoll)
	r.Post("/internal/update/status", a.handleUpdateStatus)
	r.Get("/internal/environments", a.handleEnvPoll)
	r.Post("/internal/environments/status", a.handleEnvStatus)
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", a.handleLogin(store.KindAdmin))
		r.Post("/login/mfa", a.handleMFA(store.KindAdmin))
		r.Post("/logout", a.handleLogout(store.KindAdmin))
		r.Post("/setup", a.publicPost(a.handleToken(auth.TokenAdminSetup)))
		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth(store.KindAdmin), a.enforceAdmin2FA)
			r.Get("/me", a.handleMe)
			r.Post("/logout-all", a.handleLogoutAll)
			r.Post("/password/change", a.handleChangePassword)
			r.Post("/2fa/begin", a.handle2FABegin)
			r.Post("/2fa/confirm", a.handle2FAConfirm)

			r.Get("/admins", a.adminAccounts)
			r.Post("/admins", a.adminAccountInvite)
			r.Post("/admins/{id}/reset", a.adminAccountReset)
			r.Patch("/admins/{id}", a.adminAccountPatch)
			r.Delete("/admins/{id}", a.adminAccountDelete)

			r.Get("/users", a.adminUsers)
			r.Post("/users", a.adminInvite)
			r.Post("/users/{id}/resend", a.adminResend)
			r.Patch("/users/{id}", a.adminUserPatch)
			r.Get("/users/{id}/settings", a.adminUserSettings)
			r.Put("/users/{id}/hooks", a.adminUserHooksPut)
			r.Put("/users/{id}/commands", a.adminUserCommandsPut)
			r.Put("/users/{id}/mcp", a.adminUserMCPSet)
			r.Delete("/users/{id}/notes/{note}", a.adminUserNoteDelete)
			r.Delete("/users/{id}/connections/{kind}", a.adminUserConnectionDelete)
			r.Delete("/users/{id}/automations/{aid}", a.adminUserAutomationDelete)
			r.Delete("/users/{id}/tokens/{tid}", a.adminUserTokenRevoke)
			r.Post("/users/{id}/topup", a.adminTopUp)
			r.Post("/users/{id}/reset-usage", a.adminResetUsage)

			r.Get("/levels", a.adminLevels)
			r.Put("/levels/{name}", a.adminLevelPut)
			r.Get("/groups", a.adminGroups)
			r.Post("/groups", a.adminGroupSave)
			r.Put("/groups/{id}", a.adminGroupSave)
			r.Delete("/groups/{id}", a.adminGroupDelete)

			r.Get("/providers", a.adminProviders)
			r.Post("/providers", a.adminProviderSave)
			r.Put("/providers/{id}", a.adminProviderSave)
			r.Delete("/providers/{id}", a.adminProviderDelete)
			r.Post("/providers/{id}/refresh", a.adminProviderRefresh)
			r.Get("/models", a.adminModels)
			r.Post("/models/{id}/enabled", a.adminModelEnabled)
			r.Post("/models/{id}/recommended", a.adminModelRecommended)
			r.Post("/models/{id}/benchmark", a.adminBenchmarkRun)
			r.Post("/models/{id}/probe", a.adminModelProbe)
			r.Get("/update", a.adminUpdateInfo)
			r.Post("/update", a.adminUpdateRequest)
			r.Get("/environment-catalog", a.adminEnvCatalog)
			r.Post("/environment-catalog/{name}/build", a.adminEnvBuild)
			r.Post("/environment-catalog/{name}/remove", a.adminEnvRemove)
			r.Put("/environment-settings", a.adminEnvSettings)
			r.Get("/image-templates", a.adminImageTemplates)
			r.Put("/image-templates/{name}", a.adminImageTemplateSet)
			r.Delete("/image-templates/{name}", a.adminImageTemplateDelete)
			r.Put("/providers/{id}/helper", a.adminProviderHelper)
			r.Get("/benchmarks", a.adminBenchmarks)
			r.Get("/benchmarks/runs", a.adminBenchmarkRuns)
			r.Get("/mcp/search", a.adminMCPSearch)
			r.Get("/mcp/suggestions", a.adminMCPSuggestions)
			r.Post("/mcp/approve", a.adminMCPApprove)
			r.Get("/mcp/approved", a.adminMCPApproved)
			r.Delete("/mcp/approved", a.adminMCPRevoke)
			r.Get("/mcp/requests", a.adminMCPRequests)
			r.Delete("/mcp/requests/{id}", a.adminMCPRequestDismiss)
			r.Get("/secrets", a.adminSecretNames)
			r.Put("/secrets/{name}", a.adminSecretPut)
			r.Delete("/secrets/{name}", a.adminSecretDelete)

			r.Get("/sessions", a.adminSessions)
			r.Get("/sessions/{id}/messages", a.adminMessages)
			r.Get("/audit", a.adminAudit)
			r.Get("/audit/export", a.adminAuditExport)
			r.Get("/egress", a.adminEgress)
			r.Get("/health", a.adminHealth)
			r.Post("/health/snooze", a.adminSnooze)
			r.Post("/kill", a.adminKill)
			r.Post("/kill/release", a.adminKillRelease)
			r.Get("/settings", a.adminSettingsGet)
			r.Put("/settings", a.adminSettingsPut)
			r.Get("/config", a.adminConfigGet)
			r.Put("/config", a.adminConfigPut)
			r.Delete("/config/{key}", a.adminConfigReset)
			r.Post("/mail/test", a.adminMailTest)
		})
	})
	if a.AdminWeb != nil {
		r.NotFound(a.AdminWeb.ServeHTTP)
	}
	return r
}

// enforceAdmin2FA: admin accounts must have 2FA before anything else works.
func (a *App) enforceAdmin2FA(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principal(r).Account.TOTPEnabled {
			switch r.URL.Path {
			case "/api/me", "/api/2fa/begin", "/api/2fa/confirm", "/api/logout":
			default:
				writeErr(w, http.StatusForbidden, "2fa_required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) actor(r *http.Request) string  { return "admin:" + itoa(principal(r).Account.ID) }
func (a *App) actorID(r *http.Request) int64 { return principal(r).Account.ID }

func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return n
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	us, _ := a.Store.ListUsers(r.Context())
	out := []map[string]any{}
	for _, u := range us {
		out = append(out, map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "disabled": u.Disabled, "totp_enabled": u.TOTPEnabled,
			"invited": u.PasswordHash == "", "quota_daily_tokens": u.QuotaDailyTokens, "quota_monthly_tokens": u.QuotaMonthlyTokens,
			"max_sessions": u.MaxSessions, "egress_mode": u.EgressMode, "allowed_models": u.AllowedModels, "cloud_tools_disabled": u.CloudToolsDisabled, "terminal_disabled": u.TerminalDisabled,
			"level": u.Level, "group_id": u.GroupID, "quota_cpu_minutes": u.QuotaCPUMinutes})
	}
	writeJSON(w, 200, out)
}

func (a *App) adminInvite(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	id, link, err := a.Auth.InviteUser(r.Context(), a.actorID(r), b.Email, b.Name)
	if err != nil && id == 0 {
		code := 400
		if errors.Is(err, store.ErrConflict) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	// the link goes to the admin who made the invite, so it can be passed on by hand
	resp := map[string]any{"id": id, "invite_url": link}
	if err != nil { // user created but email failed
		resp["warning"] = "user created but the invite email could not be sent: " + err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (a *App) adminResend(w http.ResponseWriter, r *http.Request) {
	link, err := a.Auth.ResendInvite(r.Context(), a.actorID(r), pathID(r))
	resp := map[string]any{"ok": err == nil, "invite_url": link}
	if err != nil {
		if link == "" {
			writeErr(w, 400, err.Error())
			return
		}
		resp["warning"] = "the invite email could not be sent: " + err.Error()
	}
	writeJSON(w, 200, resp)
}

func (a *App) adminUserPatch(w http.ResponseWriter, r *http.Request) {
	u, err := a.Store.GetUser(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Disabled           *bool     `json:"disabled"`
		QuotaDaily         *int64    `json:"quota_daily_tokens"`
		QuotaMonthly       *int64    `json:"quota_monthly_tokens"`
		MaxSessions        *int      `json:"max_sessions"`
		EgressMode         *string   `json:"egress_mode"`
		AllowedModels      *[]string `json:"allowed_models"`
		CloudToolsDisabled *bool     `json:"cloud_tools_disabled"`
		TerminalDisabled   *bool     `json:"terminal_disabled"`
		QuotaCPUMinutes    *int64    `json:"quota_cpu_minutes"`
		Level              *string   `json:"level"`
		GroupID            *int64    `json:"group_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.EgressMode != nil && *b.EgressMode != "" && *b.EgressMode != "open" && *b.EgressMode != "allowlist" && *b.EgressMode != "none" {
		writeErr(w, 400, "invalid egress mode")
		return
	}
	if b.Level != nil {
		switch *b.Level {
		case store.LevelUser, store.LevelPowerUser:
		case store.LevelAdmin:
			// an admin-level chat account must belong to a real admin: the same person, found by email
			if _, err := a.Store.GetAccountByEmail(r.Context(), store.KindAdmin, u.Email); err != nil {
				writeErr(w, 400, "there is no admin account with this email address, so this user cannot be given the admin level")
				return
			}
		default:
			writeErr(w, 400, "invalid level")
			return
		}
		u.Level = *b.Level
	}
	if b.GroupID != nil {
		if *b.GroupID != 0 {
			if p, err := a.Store.GetPolicyByID(r.Context(), *b.GroupID); err != nil || p.Kind != "group" {
				writeErr(w, 400, "no such group")
				return
			}
		}
		u.GroupID = *b.GroupID
	}
	if b.QuotaCPUMinutes != nil {
		u.QuotaCPUMinutes = *b.QuotaCPUMinutes
	}
	if b.QuotaDaily != nil {
		u.QuotaDailyTokens = *b.QuotaDaily
	}
	if b.QuotaMonthly != nil {
		u.QuotaMonthlyTokens = *b.QuotaMonthly
	}
	if b.MaxSessions != nil {
		u.MaxSessions = *b.MaxSessions
	}
	if b.EgressMode != nil {
		u.EgressMode = *b.EgressMode
	}
	if b.AllowedModels != nil {
		u.AllowedModels = *b.AllowedModels
	}
	if b.TerminalDisabled != nil {
		u.TerminalDisabled = *b.TerminalDisabled
	}
	if b.CloudToolsDisabled != nil {
		u.CloudToolsDisabled = *b.CloudToolsDisabled
	}
	if err := a.Store.UpdateUserPolicy(r.Context(), *u); err != nil {
		writeErr(w, 500, "could not update")
		return
	}
	if b.Disabled != nil && *b.Disabled != u.Disabled {
		acc := u.Account
		acc.Disabled = *b.Disabled
		_ = a.Store.UpdateAccount(r.Context(), acc)
		if *b.Disabled {
			_ = a.Store.DeleteAuthSessionsFor(r.Context(), store.KindUser, u.ID)
			if ss, err := a.Store.ListChatSessions(r.Context(), u.ID); err == nil {
				for _, cs := range ss {
					if run := a.Runs.Get(cs.ID); run != nil {
						run.Stop()
					}
					a.Terms.CloseSession(cs.ID, "Your account was disabled.")
				}
			}
			if a.Sandbox != nil {
				_, _ = a.Sandbox.KillUser(r.Context(), u.ID)
			}
		}
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "user.update", "user:"+itoa(u.ID), "policy changed")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminTopUp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Tokens int64 `json:"tokens"`
	}
	if !decode(w, r, &b) || b.Tokens <= 0 {
		writeErr(w, 400, "tokens must be positive")
		return
	}
	if err := a.Quota.TopUp(r.Context(), a.actorID(r), pathID(r), b.Tokens); err != nil {
		writeErr(w, 500, "top-up failed")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminResetUsage(w http.ResponseWriter, r *http.Request) {
	if err := a.Quota.Reset(r.Context(), a.actorID(r), pathID(r)); err != nil {
		writeErr(w, 500, "reset failed")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- providers & models ----

func (a *App) adminProviders(w http.ResponseWriter, r *http.Request) {
	ps, _ := a.Store.ListProviders(r.Context())
	pt := a.probes(r.Context())
	out := []map[string]any{}
	for _, p := range ps {
		stream := map[string]any{}
		if s, ok := pt.provider[p.ID]; ok {
			stream = map[string]any{"ok": s.OK, "detail": s.Detail, "at": s.At}
		}
		out = append(out, map[string]any{"helper_model": a.helperModel(r.Context(), p.ID), "helper_assert": a.settingBool(r.Context(), assertKey(p.ID), false), "stream_probe": stream, "id": p.ID, "name": p.Name, "kind": p.Kind, "base_url": p.BaseURL, "concurrency": p.Concurrency, "enabled": p.Enabled, "has_key": len(p.APIKeyEnc) > 0})
	}
	writeJSON(w, 200, out)
}

func (a *App) adminProviderSave(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		Concurrency int    `json:"concurrency"`
		Enabled     bool   `json:"enabled"`
	}
	b.Enabled = true
	if !decode(w, r, &b) {
		return
	}
	if b.Name == "" || b.BaseURL == "" {
		writeErr(w, 400, "name and base_url are required")
		return
	}
	switch b.Kind {
	case provider.KindOllama, provider.KindLlamaCpp, provider.KindLiteLLM, provider.KindOpenAI:
	default:
		writeErr(w, 400, "unknown provider kind")
		return
	}
	p := store.Provider{ID: pathID(r), Name: b.Name, Kind: b.Kind, BaseURL: b.BaseURL, Concurrency: b.Concurrency, Enabled: b.Enabled}
	if p.ID != 0 {
		old, err := a.Store.GetProvider(r.Context(), p.ID)
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		p.APIKeyEnc = old.APIKeyEnc // keep the stored key unless a new one is sent
	}
	if b.APIKey != "" {
		enc, err := a.Secrets.Vault.Seal([]byte(b.APIKey), fmt.Sprintf("provider:%s", b.Name))
		if err != nil {
			writeErr(w, 500, "could not encrypt the key")
			return
		}
		p.APIKeyEnc = enc
	}
	id, err := a.Store.UpsertProvider(r.Context(), p)
	if err != nil {
		code := 500
		if errors.Is(err, store.ErrConflict) {
			code = 409
		}
		writeErr(w, code, "could not save provider")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "provider.save", "provider:"+itoa(id), b.Name)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (a *App) adminProviderDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteProvider(r.Context(), pathID(r)); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "provider.delete", "provider:"+itoa(pathID(r)), "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ProviderFor builds an adapter for a stored provider (decrypting its key).
func (a *App) ProviderFor(p *store.Provider) (provider.Provider, error) {
	if a.NewProvider != nil {
		return a.NewProvider(p)
	}
	key := ""
	if len(p.APIKeyEnc) > 0 {
		pt, err := a.Secrets.Vault.Open(p.APIKeyEnc, "provider:"+p.Name)
		if err != nil {
			return nil, err
		}
		key = string(pt)
	}
	return provider.New(provider.Config{Kind: p.Kind, BaseURL: p.BaseURL, APIKey: key})
}

func (a *App) adminProviderRefresh(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.GetProvider(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	n, err := a.RefreshModels(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "provider error: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"models": n})
}

// RefreshModels discovers models (read-only toward the LLM server).
func (a *App) RefreshModels(ctx context.Context, p *store.Provider) (int, error) {
	pr, err := a.ProviderFor(p)
	if err != nil {
		return 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ms, err := pr.ListModels(cctx)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		tools := -1
		if d, err := pr.ModelDetail(cctx, m.Name); err == nil && d.SupportsTools != nil {
			tools = 0
			if *d.SupportsTools {
				tools = 1
			}
		}
		if _, err := a.Store.UpsertModel(ctx, store.Model{ProviderID: p.ID, Name: m.Name, Digest: m.Digest, ToolsSupported: tools, SeenAt: a.Now()}); err != nil {
			return 0, err
		}
	}
	return len(ms), nil
}

func (a *App) adminModels(w http.ResponseWriter, r *http.Request) {
	ms, _ := a.Store.ListModels(r.Context(), 0)
	pt := a.probes(r.Context())
	out := []map[string]any{}
	for _, m := range ms {
		probe := map[string]any{}
		if p, ok := pt.tools[fmt.Sprintf("%d/%s", m.ProviderID, m.Name)]; ok {
			probe = map[string]any{"ok": p.OK, "detail": p.Detail, "at": p.At}
		}
		out = append(out, map[string]any{"tool_probe": probe, "id": m.ID, "provider_id": m.ProviderID, "name": m.Name, "enabled": m.Enabled, "context_override": m.ContextOverride, "digest": m.Digest, "tools": m.ToolsSupported, "recommended": m.Recommended})
	}
	writeJSON(w, 200, out)
}

func (a *App) adminModelEnabled(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := a.Store.SetModelEnabled(r.Context(), pathID(r), b.Enabled); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- metadata, audit, egress ----

// adminSessions shows metadata only: who, when, model, tokens, state. Titles
// and title are shown; the admin can open the messages (every read is audited).
func (a *App) adminSessions(w http.ResponseWriter, r *http.Request) {
	ss, _ := a.Store.ListAllChatSessions(r.Context())
	out := []map[string]any{}
	for _, s := range ss {
		m := map[string]any{"id": s.ID, "user_id": s.UserID, "title": s.Title, "model": s.Model, "state": s.State, "sandbox": s.SandboxID != "",
			"created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "tokens_in": s.TokensIn, "tokens_out": s.TokensOut}
		out = append(out, m)
	}
	writeJSON(w, 200, out)
}

func (a *App) adminMessages(w http.ResponseWriter, r *http.Request) {
	ms, _ := a.Store.ListMessages(r.Context(), chi.URLParam(r, "id"), false)
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "admin.read_content", "session:"+chi.URLParam(r, "id"), "")
	writeJSON(w, 200, ms)
}

func (a *App) adminAudit(w http.ResponseWriter, r *http.Request) {
	es, err := a.Store.ListAudit(r.Context(), 0)
	if err != nil {
		writeErr(w, 500, "could not load audit log")
		return
	}
	if es == nil {
		es = []audit.Entry{}
	}
	bad := audit.Verify(es)
	head := audit.HeadOf(es)
	total := len(es)
	if lim := 500; len(es) > lim {
		es = es[len(es)-lim:]
	}
	// head is the whole chain in one value: keep it somewhere else and a rewrite of the log, or
	// entries cut off its end, shows up as a different head later.
	writeJSON(w, 200, map[string]any{"entries": es, "chain_ok": bad == -1, "first_bad": bad, "head": head, "total": total, "genesis": audit.GenesisHash})
}

// adminAuditExport sends the whole log as JSON Lines so it can be checked on another machine with
// `syntaxis audit verify`, without trusting this server's own verdict.
func (a *App) adminAuditExport(w http.ResponseWriter, r *http.Request) {
	es, err := a.Store.ListAudit(r.Context(), 0)
	if err != nil {
		writeErr(w, 500, "could not load audit log")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="syntaxis-audit.jsonl"`)
	_ = audit.WriteLines(w, es)
}

func (a *App) adminEgress(w http.ResponseWriter, r *http.Request) {
	es, _ := a.Store.ListEgress(r.Context(), 0, 1000) // all users
	if es == nil {
		es = []store.EgressEntry{}
	}
	writeJSON(w, 200, es)
}

func (a *App) adminHealth(w http.ResponseWriter, r *http.Request) {
	fs := a.healthFindings(r.Context())
	writeJSON(w, 200, map[string]any{"findings": fs, "summary": health.Summarise(fs)})
}

func (a *App) adminSnooze(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CheckID string `json:"check_id"`
		Subject string `json:"subject"`
		Hours   int    `json:"hours"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Hours <= 0 || b.Hours > 24*30 {
		b.Hours = 24
	}
	for _, f := range a.healthFindings(r.Context()) {
		if f.CheckID == b.CheckID && f.Subject == b.Subject {
			if !a.Health.Snooze(f, a.Now().Add(time.Duration(b.Hours)*time.Hour)) {
				writeErr(w, 400, "errors cannot be snoozed")
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	writeErr(w, 404, "no such finding")
}

func (a *App) adminKill(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"user_id"`
	}
	_ = decodeOptional(r, &b)
	if a.Sandbox == nil {
		writeErr(w, 503, "sandboxes are not available")
		return
	}
	var n int
	var err error
	if b.UserID != 0 {
		n, err = a.Sandbox.KillUser(r.Context(), b.UserID)
		_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "killswitch.user", "user:"+itoa(b.UserID), fmt.Sprint("stopped=", n))
	} else {
		n, err = a.Sandbox.KillAll(r.Context())
		_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "killswitch.all", "", fmt.Sprint("stopped=", n))
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"stopped": n})
}

func (a *App) adminKillRelease(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"user_id"`
	}
	_ = decodeOptional(r, &b)
	if a.Sandbox == nil {
		writeErr(w, 503, "sandboxes are not available")
		return
	}
	if b.UserID != 0 {
		a.Sandbox.ReleaseUser(b.UserID)
	} else {
		a.Sandbox.ReleaseKill()
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "killswitch.release", "user:"+itoa(b.UserID), "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminSettingsGet(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for k, def := range settingDefaults {
		// an option the environment can also set shows the value in effect (saved, else environment,
		// else default), not the built-in default: otherwise the form lies about what is running
		if _, ok := settings.ByKey(k); ok {
			out[k] = settings.Effective(r.Context(), a.Store, os.Getenv, k)
			continue
		}
		v, _ := a.Store.GetSetting(r.Context(), k, def)
		out[k] = v
	}
	writeJSON(w, 200, out)
}

var settingDefaults = map[string]string{
	SettingRotationDays: "30", SettingScanMode: "warn", SettingUserOverride: "true", SettingDailyTokens: "0", SettingMonthlyTokens: "0",
	SettingVisionModels: "", SettingSearchURL: "",
	SettingUploadsEnabled: "false", SettingUploadMaxMB: "10", SettingUploadKinds: "images,pdf,documents,text",
}

func validSetting(k, v string) bool {
	switch k {
	case SettingScanMode:
		return v == "off" || v == "warn" || v == "block"
	case SettingUserOverride:
		return v == "true" || v == "false"
	case SettingVisionModels:
		return len(v) <= 2000 && !strings.ContainsAny(v, "\n\r")
	case SettingSearchURL:
		return v == "" || validSearchURL(v)
	case SettingUploadsEnabled:
		return v == "true" || v == "false"
	case SettingUploadMaxMB:
		n, err := strconv.Atoi(v)
		return err == nil && n >= 1 && n <= maxUploadMBLimit
	case SettingUploadKinds:
		_, err := uploadcheck.ParseKinds(v)
		return err == nil
	case SettingRotationDays, SettingDailyTokens, SettingMonthlyTokens:
		n, err := strconv.Atoi(v)
		return err == nil && n >= 0
	}
	return false
}

func (a *App) adminSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b map[string]string
	if !decode(w, r, &b) {
		return
	}
	for k, v := range b {
		if _, ok := settingDefaults[k]; !ok || !validSetting(k, v) {
			writeErr(w, 400, "invalid setting "+k)
			return
		}
	}
	for k, v := range b {
		_ = a.Store.SetSetting(r.Context(), k, v)
		_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "setting.change", k, v)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminMailTest(w http.ResponseWriter, r *http.Request) {
	acc := principal(r).Account
	// an unreachable mail server must not hang the button: give it half a minute
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.Mail.Send(ctx, mail.Message{To: acc.Email, Subject: "Syntaxis test email", Body: "If you can read this, outbound email works."})
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.New("the mail server did not answer within 30 seconds")
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, "sending failed: "+err.Error())
		return
	}
	host, _, _, _, _ := settings.SMTPFrom(r.Context(), a.Store, a.settingSecrets(), os.Getenv)
	writeJSON(w, 200, map[string]any{"ok": true, "to": acc.Email, "sent": host != ""})
}

func (a *App) adminModelRecommended(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Recommended bool `json:"recommended"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := a.Store.SetModelRecommended(r.Context(), pathID(r), b.Recommended); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) adminBenchmarks(w http.ResponseWriter, r *http.Request) {
	bs, _ := a.Store.ListBenchmarks(r.Context(), 0)
	out := []map[string]any{}
	for _, b := range bs {
		rep := json.RawMessage(b.Result)
		out = append(out, map[string]any{"id": b.ID, "model_id": b.ModelID, "time": b.Time, "digest": b.Digest, "report": rep})
	}
	writeJSON(w, 200, out)
}

// benchRun is a benchmark that is running, or one that could not start. A finished one is removed:
// its result is in the benchmark list.
type benchRun struct {
	ModelID int64     `json:"model_id"`
	State   string    `json:"state"` // running | failed
	Started time.Time `json:"started"`
	Message string    `json:"message,omitempty"`
}

// adminBenchmarkRuns lists benchmarks that are running or failed to start, so the benchmark screen
// can show that something is happening (a run takes minutes) and why one produced no result.
func (a *App) adminBenchmarkRuns(w http.ResponseWriter, _ *http.Request) {
	out := []benchRun{}
	a.benchRuns.Range(func(_, v any) bool {
		out = append(out, v.(benchRun))
		return true
	})
	writeJSON(w, 200, out)
}

// adminBenchmarkRun starts a benchmark in the background at the lowest queue
// priority, in a throwaway sandbox that is destroyed afterwards.
func (a *App) adminBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	m, err := a.Store.GetModel(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	prov, err := a.Store.GetProvider(r.Context(), m.ProviderID)
	if err != nil || a.Sandbox == nil {
		writeErr(w, 503, "benchmarks need a provider and sandboxes")
		return
	}
	pr, err := a.ProviderFor(prov)
	if err != nil {
		writeErr(w, 502, "could not connect to the provider")
		return
	}
	actor := a.actor(r)
	id := "bench-" + newID()
	if cur, ok := a.benchRuns.Load(m.ID); ok && cur.(benchRun).State == "running" {
		writeErr(w, http.StatusConflict, "a benchmark of this model is already running")
		return
	}
	a.benchRuns.Store(m.ID, benchRun{ModelID: m.ID, State: "running", Started: a.Now()})
	go func() { //nolint:gosec // the benchmark must outlive the HTTP request that started it
		ctx := context.Background()
		cid, err := a.Sandbox.Create(ctx, 0, id, nil, false)
		if err != nil {
			a.Log.Warn("benchmark: no sandbox", "err", err)
			a.benchRuns.Store(m.ID, benchRun{ModelID: m.ID, State: "failed", Started: a.Now(), Message: "could not start a sandbox for the benchmark: " + err.Error()})
			return
		}
		defer func() {
			_ = a.Sandbox.Destroy(ctx, id, cid)
			_ = a.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(0))
		}()
		q := a.Queues
		if q == nil {
			q = NewQueues()
			a.Queues = q
		}
		chat := func(c context.Context, req provider.ChatRequest, on func(string)) (*provider.ChatResult, error) {
			tk, err := q.For(prov).Enqueue(0, queue.Benchmark)
			if err != nil {
				return nil, err
			}
			release, err := tk.Wait(c)
			if err != nil {
				return nil, err
			}
			defer release()
			return pr.Chat(c, req, on)
		}
		window := m.ContextOverride
		if window == 0 {
			window = DefaultWindow
		}
		rep := bench.Run(ctx, bench.Env{Model: m.Name, Digest: m.Digest, Window: window, Chat: chat,
			Exec: func(c context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
				return a.Sandbox.Exec(c, id, cid, e)
			}}, bench.Suite())
		_ = a.Store.AddBenchmark(ctx, m.ID, m.Digest, rep.JSON())
		a.benchRuns.Delete(m.ID)
		_, _ = a.Store.AppendAudit(ctx, actor, "benchmark.run", fmt.Sprintf("model:%d", m.ID), fmt.Sprintf("pass=%.0f%%", rep.PassRate*100))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
