// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kolonuk/syntaxis/internal/buildinfo"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/audit"
	"github.com/kolonuk/syntaxis/internal/bench"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/queue"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Setting keys editable by admins.
const (
	SettingRotationDays  = "password_rotation_days"
	SettingScanMode      = "secret_scan_mode"
	SettingUserOverride  = "secret_scan_allow_user_override"
	SettingDailyTokens   = "default_daily_tokens"
	SettingMonthlyTokens = "default_monthly_tokens"
)

// AdminHandler returns the admin app: separate routes, cookie and credentials.
func (a *App) AdminHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.secureHeaders, a.logRequests)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": buildinfo.Get()})
	})
	r.Get("/api/version", handleVersion)
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", a.handleLogin(store.KindAdmin))
		r.Post("/login/mfa", a.handleMFA(store.KindAdmin))
		r.Post("/logout", a.handleLogout(store.KindAdmin))
		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth(store.KindAdmin), a.enforceAdmin2FA)
			r.Get("/me", a.handleMe)
			r.Post("/logout-all", a.handleLogoutAll)
			r.Post("/password/change", a.handleChangePassword)
			r.Post("/2fa/begin", a.handle2FABegin)
			r.Post("/2fa/confirm", a.handle2FAConfirm)

			r.Get("/users", a.adminUsers)
			r.Post("/users", a.adminInvite)
			r.Post("/users/{id}/resend", a.adminResend)
			r.Patch("/users/{id}", a.adminUserPatch)
			r.Post("/users/{id}/topup", a.adminTopUp)
			r.Post("/users/{id}/reset-usage", a.adminResetUsage)

			r.Get("/providers", a.adminProviders)
			r.Post("/providers", a.adminProviderSave)
			r.Put("/providers/{id}", a.adminProviderSave)
			r.Delete("/providers/{id}", a.adminProviderDelete)
			r.Post("/providers/{id}/refresh", a.adminProviderRefresh)
			r.Get("/models", a.adminModels)
			r.Post("/models/{id}/enabled", a.adminModelEnabled)
			r.Post("/models/{id}/recommended", a.adminModelRecommended)
			r.Post("/models/{id}/benchmark", a.adminBenchmarkRun)
			r.Get("/benchmarks", a.adminBenchmarks)
			r.Get("/mcp/search", a.adminMCPSearch)
			r.Post("/mcp/approve", a.adminMCPApprove)
			r.Get("/mcp/approved", a.adminMCPApproved)
			r.Delete("/mcp/approved", a.adminMCPRevoke)
			r.Get("/secrets", a.adminSecretNames)
			r.Put("/secrets/{name}", a.adminSecretPut)
			r.Delete("/secrets/{name}", a.adminSecretDelete)

			r.Get("/sessions", a.adminSessions)
			r.Get("/sessions/{id}/messages", a.adminMessages)
			r.Get("/audit", a.adminAudit)
			r.Get("/egress", a.adminEgress)
			r.Get("/health", a.adminHealth)
			r.Post("/health/snooze", a.adminSnooze)
			r.Post("/kill", a.adminKill)
			r.Post("/kill/release", a.adminKillRelease)
			r.Get("/settings", a.adminSettingsGet)
			r.Put("/settings", a.adminSettingsPut)
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
			"max_sessions": u.MaxSessions, "egress_mode": u.EgressMode, "allowed_models": u.AllowedModels, "cloud_tools_disabled": u.CloudToolsDisabled, "terminal_disabled": u.TerminalDisabled})
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
	id, err := a.Auth.InviteUser(r.Context(), a.actorID(r), b.Email, b.Name)
	if err != nil && id == 0 {
		code := 400
		if errors.Is(err, store.ErrConflict) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	resp := map[string]any{"id": id}
	if err != nil { // user created but email failed
		resp["warning"] = "user created but the invite email could not be sent: " + err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (a *App) adminResend(w http.ResponseWriter, r *http.Request) {
	if err := a.Auth.ResendInvite(r.Context(), a.actorID(r), pathID(r)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
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
	}
	if !decode(w, r, &b) {
		return
	}
	if b.EgressMode != nil && *b.EgressMode != "" && *b.EgressMode != "open" && *b.EgressMode != "allowlist" && *b.EgressMode != "none" {
		writeErr(w, 400, "invalid egress mode")
		return
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
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "base_url": p.BaseURL, "concurrency": p.Concurrency, "enabled": p.Enabled, "has_key": len(p.APIKeyEnc) > 0})
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
	out := []map[string]any{}
	for _, m := range ms {
		out = append(out, map[string]any{"id": m.ID, "provider_id": m.ProviderID, "name": m.Name, "enabled": m.Enabled, "context_override": m.ContextOverride, "digest": m.Digest, "tools": m.ToolsSupported, "recommended": m.Recommended})
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
// and content are user data and are withheld unless ALLOW_ADMIN_READ_CONTENT.
func (a *App) adminSessions(w http.ResponseWriter, r *http.Request) {
	ss, _ := a.Store.ListAllChatSessions(r.Context())
	out := []map[string]any{}
	for _, s := range ss {
		m := map[string]any{"id": s.ID, "user_id": s.UserID, "model": s.Model, "state": s.State, "sandbox": s.SandboxID != "",
			"created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "tokens_in": s.TokensIn, "tokens_out": s.TokensOut}
		if a.Cfg.AdminReadMsg {
			m["title"] = s.Title
		}
		out = append(out, m)
	}
	writeJSON(w, 200, out)
}

func (a *App) adminMessages(w http.ResponseWriter, r *http.Request) {
	if !a.Cfg.AdminReadMsg {
		writeErr(w, http.StatusForbidden, "message content is private (ALLOW_ADMIN_READ_CONTENT is false)")
		return
	}
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
	if lim := 500; len(es) > lim {
		es = es[len(es)-lim:]
	}
	writeJSON(w, 200, map[string]any{"entries": es, "chain_ok": bad == -1, "first_bad": bad})
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
		v, _ := a.Store.GetSetting(r.Context(), k, def)
		out[k] = v
	}
	writeJSON(w, 200, out)
}

var settingDefaults = map[string]string{
	SettingRotationDays: "30", SettingScanMode: "warn", SettingUserOverride: "true", SettingDailyTokens: "0", SettingMonthlyTokens: "0",
}

func validSetting(k, v string) bool {
	switch k {
	case SettingScanMode:
		return v == "off" || v == "warn" || v == "block"
	case SettingUserOverride:
		return v == "true" || v == "false"
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
	err := a.Mail.Send(r.Context(), mail.Message{To: acc.Email, Subject: "Syntaxis test email", Body: "If you can read this, outbound email works."})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "sending failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
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
	go func() { //nolint:gosec // the benchmark must outlive the HTTP request that started it
		ctx := context.Background()
		cid, err := a.Sandbox.Create(ctx, 0, id, nil, false)
		if err != nil {
			a.Log.Warn("benchmark: no sandbox", "err", err)
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
		_, _ = a.Store.AppendAudit(ctx, actor, "benchmark.run", fmt.Sprintf("model:%d", m.ID), fmt.Sprintf("pass=%.0f%%", rep.PassRate*100))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
