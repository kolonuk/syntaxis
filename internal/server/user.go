// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/kolonuk/syntaxis/internal/buildinfo"
	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/policy"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/uploadcheck"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/store"
)

// RunFunc executes the agent for a session message. It is wired in main (or a
// stub in tests). It must emit events on run and honour ctx cancellation.
type RunFunc func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, message string)

// UserHandler returns the chat-user app. It contains no admin routes at all.
func (a *App) UserHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.secureHeaders, a.logRequests)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": buildinfo.Get()})
	})
	r.Post("/hooks/{id}/{token}", a.handleHook)  // called by outside services; the secret is in the address
	r.Get("/api/shared/{token}", a.handleShared) // public, read-only; the secret is in the address
	r.Get("/api/version", handleVersion)

	r.Route("/api", func(r chi.Router) {
		r.Post("/login", a.handleLogin(store.KindUser))
		r.Post("/login/mfa", a.handleMFA(store.KindUser))
		r.Post("/logout", a.handleLogout(store.KindUser))
		r.Post("/password/forgot", a.publicPost(a.handleForgot))
		r.Post("/password/reset", a.publicPost(a.handleToken(auth.TokenReset)))
		r.Post("/invite/accept", a.publicPost(a.handleToken(auth.TokenInvite)))

		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth(store.KindUser), a.enforceRotation)
			r.Get("/me", a.handleMe)
			r.Post("/logout-all", a.handleLogoutAll)
			r.Post("/password/change", a.handleChangePassword)
			r.Post("/2fa/begin", a.handle2FABegin)
			r.Post("/2fa/confirm", a.handle2FAConfirm)
			r.Post("/2fa/disable", a.handle2FADisable)
			r.Get("/models", a.handleModels)
			r.Get("/usage", a.handleUsage)
			r.Get("/egress", a.handleMyEgress)
			r.Get("/tokens", a.handleTokensList)
			r.Post("/tokens", a.handleTokenCreate)
			r.Delete("/tokens/{id}", a.handleTokenRevoke)
			r.Get("/secrets", a.handleSecretNames)
			r.Put("/secrets/{name}", a.handleSecretPut)
			r.Delete("/secrets/{name}", a.handleSecretDelete)
			r.Post("/workspace/keep", a.handleWorkspaceKeep)
			r.Get("/automations", a.handleAutomations)
			r.Get("/automation-kinds", a.handleAutomationKinds)
			r.Post("/automations", a.handleAutomationCreate)
			r.Put("/automations/{id}", a.handleAutomationUpdate)
			r.Delete("/automations/{id}", a.handleAutomationDelete)
			r.Post("/automations/{id}/run", a.handleAutomationRunNow)
			r.Post("/automations/{id}/token", a.handleAutomationToken)
			r.Get("/automations/{id}/runs", a.handleAutomationRuns)
			r.Get("/commands", a.handleMyCommands)
			r.Put("/commands", a.handleMyCommandsPut)
			r.Get("/hooks", a.handleMyHooks)
			r.Put("/hooks", a.handleMyHooksPut)
			r.Get("/memory", a.handleMemoryList)
			r.Post("/memory", a.handleMemoryAdd)
			r.Put("/memory/{id}", a.handleMemoryPut)
			r.Delete("/memory/{id}", a.handleMemoryDelete)
			r.Get("/shares", a.handleShares)
			r.Delete("/shares/{id}", a.handleShareRevoke)
			r.Get("/mcp", a.handleMyMCP)
			r.Put("/mcp", a.handleMyMCPSet)
			r.Get("/connections", a.handleConnections)
			r.Put("/connections/{kind}", a.handleConnectionPut)
			r.Delete("/connections/{kind}", a.handleConnectionDelete)
			r.Post("/connections/{kind}/test", a.handleConnectionTest)
			r.Get("/connections/{kind}/repos", a.handleConnectionRepos)
			r.Post("/connections/{kind}/repos", a.handleConnectionRepoCreate)
			r.Get("/environments", a.handleEnvironments)
			r.Get("/parallel/capacity", a.handleParallelCapacity)
			r.Post("/parallel", a.handleParallelCreate)
			r.Get("/parallel/{batch}", a.handleParallelGet)
			r.Post("/parallel/{batch}/keep", a.handleParallelKeep)
			r.Get("/sessions", a.handleSessions)
			r.Post("/sessions", a.handleSessionCreate)
			r.Route("/sessions/{id}", func(r chi.Router) {
				r.Get("/", a.handleSessionGet)
				r.Patch("/", a.handleSessionPatch)
				r.Delete("/", a.handleSessionDelete)
				r.Get("/messages", a.handleMessages)
				r.Post("/messages", a.handleSend)
				r.Post("/edit", a.handleEdit)
				r.Post("/fork", a.handleFork)
				r.Get("/events", a.handleEvents)
				r.Post("/stop", a.handleStop)
				r.Post("/approvals/{call}", a.handleApproval)
				r.Post("/answer", a.handleAnswer)
				r.Post("/plan", a.handlePlan)
				r.Post("/shares", a.handleShareCreate)
				r.Get("/diff", a.handleDiff)
				r.Get("/checkpoints", a.handleCheckpoints)
				r.Get("/checkpoints/{seq}/diff", a.handleCheckpointDiff)
				r.Post("/checkpoints/{seq}/restore", a.handleCheckpointRestore)
				r.Get("/export", a.handleExport)
				r.Get("/resources", a.handleResources)
				r.Get("/terminal", a.handleTerminal)
				r.Get("/commands", a.handleSessionCommands)
				r.Get("/repo-hooks", a.handleRepoHooks)
				r.Post("/repo-hooks/trust", a.handleRepoHooksTrust)
				r.Get("/files", a.handleFileList)
				r.Get("/files/content", a.handleFileGet)
				r.Put("/files/content", a.handleFilePut)
				r.Get("/files/download", a.handleFileDownload)
				r.Post("/files/upload", a.handleFileUpload)
				r.Post("/files/mkdir", a.handleFileMkdir)
				r.Delete("/files", a.handleFileDelete)
				r.Get("/mcp", a.handleSessionMCP)
				r.Put("/mcp", a.handleSessionMCPSet)
			})
		})
	})
	// authenticated dev-server preview (owner only)
	r.Group(func(r chi.Router) {
		r.Use(a.requireAuth(store.KindUser), a.enforceRotation)
		r.HandleFunc("/preview/{id}/{port}", a.handlePreviewRedirect)
		r.HandleFunc("/preview/{id}/{port}/*", a.handlePreview)
	})
	if a.Web != nil {
		r.NotFound(a.Web.ServeHTTP)
	}
	return r
}

func (a *App) handlePreviewRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
}

func (a *App) handleWorkspaceKeep(w http.ResponseWriter, r *http.Request) {
	if a.Lifecycle == nil {
		writeErr(w, http.StatusServiceUnavailable, "retention is not configured")
		return
	}
	var b struct {
		Keep bool `json:"keep"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := a.Lifecycle.Keep(r.Context(), principal(r).Account.ID, b.Keep); err != nil {
		writeErr(w, 500, "could not save")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// enforceRotation blocks everything except changing the password once the
// rotation policy has expired the password (users without 2FA only).
func (a *App) enforceRotation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if must, _ := a.Auth.RotationStatus(r.Context(), p.Account); must {
			switch r.URL.Path {
			case "/api/me", "/api/password/change", "/api/logout":
			default:
				writeErr(w, http.StatusForbidden, "password_expired")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleForgot(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &b) {
		return
	}
	_ = a.Auth.RequestReset(r.Context(), b.Email) // errors are not revealed
	writeJSON(w, http.StatusOK, map[string]string{"message": "If that account exists, an email with a reset link has been sent."})
}

func (a *App) handleToken(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if !decode(w, r, &b) {
			return
		}
		if _, err := a.Auth.CompleteToken(r.Context(), kind, b.Token, b.Password); err != nil {
			if errors.Is(err, auth.ErrInvalidToken) {
				writeErr(w, http.StatusBadRequest, err.Error())
			} else {
				writeErr(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	must, left := a.Auth.RotationStatus(r.Context(), p.Account)
	out := map[string]any{
		"kind": p.Account.Kind, "name": p.Account.Name, "email": p.Account.Email, "totp_enabled": p.Account.TOTPEnabled,
		"must_change_password": must, "password_days_left": left, "csrf": p.Session.CSRF,
		"remind_2fa": p.Account.Kind == store.KindUser && !p.Account.TOTPEnabled,
	}
	if t := tokenFrom(r); t != nil {
		out["api_token"] = map[string]any{"name": t.Name, "scope": t.Scope}
	}
	if p.Account.Kind == store.KindUser {
		up := a.uploadLimit(r.Context())
		kinds := []string{}
		for _, k := range uploadcheck.AllKinds {
			if up.kinds[k] {
				kinds = append(kinds, k)
			}
		}
		out["uploads"] = map[string]any{"enabled": up.enabled, "max_bytes": up.maxBytes, "kinds": kinds}
		out["banners"] = health.UserBanners(a.healthFindings(r.Context()))
		if cu, err := a.Store.GetUser(r.Context(), p.Account.ID); err == nil {
			out["level"] = cu.Level
			// an admin signs in to the admin app separately (its own password and 2FA); this is only the link
			if cu.Level == store.LevelAdmin && a.Cfg.AdminURL != "" {
				out["admin_url"] = a.Cfg.AdminURL
			}
		}
	} else if p.Account.Kind == store.KindAdmin && a.Cfg.BaseURL != "" {
		out["chat_url"] = a.Cfg.BaseURL
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := a.Auth.DisableTOTP(r.Context(), principal(r), b.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) user(r *http.Request) *store.User {
	u, err := a.Store.GetUser(r.Context(), principal(r).Account.ID)
	if err != nil {
		return nil
	}
	return a.EffectiveUser(r.Context(), u)
}

// CPUExhaustedFor reports whether a user's daily compute allowance is used up, counting an
// allowance that comes from their level or group (the sandbox monitor pauses them when it is).
func (a *App) CPUExhaustedFor(ctx context.Context, userID int64) bool {
	u, err := a.Store.GetUser(ctx, userID)
	return err == nil && a.Quota.CPUExhausted(ctx, a.EffectiveUser(ctx, u))
}

// EffectiveUser returns the user with the settings of their level and group filled in
// wherever they have none of their own (see package policy).
func (a *App) EffectiveUser(ctx context.Context, u *store.User) *store.User {
	var lvl, grp *store.PolicyData
	if p, err := a.Store.GetPolicy(ctx, "level", u.Level); err == nil {
		lvl = &p.Data
	}
	if u.GroupID != 0 {
		if p, err := a.Store.GetPolicyByID(ctx, u.GroupID); err == nil {
			grp = &p.Data
		}
	}
	eff := policy.Apply(*u, lvl, grp)
	return &eff
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	provs, _ := a.Store.ListProviders(r.Context())
	pname := map[int64]string{}
	for _, p := range provs {
		if p.Enabled {
			pname[p.ID] = p.Name
		}
	}
	allowed := map[string]bool{}
	for _, m := range u.AllowedModels {
		allowed[m] = true
	}
	models, _ := a.Store.ListModels(r.Context(), 0)
	pt := a.probes(r.Context())
	out := []map[string]any{}
	for _, m := range models {
		pn, ok := pname[m.ProviderID]
		if !ok || !m.Enabled {
			continue
		}
		id := pn + "/" + m.Name
		if len(allowed) > 0 && !allowed[id] {
			continue // empty allow-list = every enabled model
		}
		out = append(out, map[string]any{"id": id, "name": m.Name, "provider": pn, "tools": pt.toolsUsable(m), "recommended": m.Recommended})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	m, err := a.Quota.Meters(r.Context(), u)
	if err != nil {
		writeErr(w, 500, "could not load usage")
		return
	}
	writeJSON(w, 200, map[string]any{
		"daily": m.Daily, "monthly": m.Monthly, "sandbox_seconds": m.SandboxSeconds, "cpu_seconds": m.CPUSeconds,
		"daily_resets_at": m.DailyResetsAt, "monthly_resets_at": m.MonthlyResetsAt,
	})
}

func (a *App) handleMyEgress(w http.ResponseWriter, r *http.Request) {
	es, err := a.Store.ListEgress(r.Context(), principal(r).Account.ID, 500) // only the caller's own
	if err != nil {
		writeErr(w, 500, "could not load egress log")
		return
	}
	if es == nil {
		es = []store.EgressEntry{}
	}
	writeJSON(w, 200, es)
}

// ---- secrets ----

func (a *App) handleSecretNames(w http.ResponseWriter, r *http.Request) {
	names, err := a.Secrets.Names(r.Context(), secrets.ScopeUser, principal(r).Account.ID)
	if err != nil {
		writeErr(w, 500, "could not list secrets")
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, 200, names) // names only, never values
}

func (a *App) handleSecretPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Value == "" {
		writeErr(w, 400, "the value is empty")
		return
	}
	if err := a.Secrets.Put(r.Context(), secrets.ScopeUser, principal(r).Account.ID, chi.URLParam(r, "name"), b.Value); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.Secrets.Delete(r.Context(), secrets.ScopeUser, principal(r).Account.ID, chi.URLParam(r, "name")); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- sessions ----

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ownSession loads a session owned by the caller; anything else is a 404 so
// existence of other users' sessions is not revealed.
func (a *App) ownSession(w http.ResponseWriter, r *http.Request) (*store.ChatSession, bool) {
	s, err := a.Store.GetChatSession(r.Context(), chi.URLParam(r, "id"))
	if err != nil || s.UserID != principal(r).Account.ID {
		writeErr(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return s, true
}

func (a *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	ss, err := a.Store.ListChatSessions(r.Context(), principal(r).Account.ID)
	if err != nil {
		writeErr(w, 500, "could not list sessions")
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	out := []map[string]any{}
	for _, s := range ss {
		if q != "" && !strings.Contains(strings.ToLower(s.Title), q) {
			continue
		}
		out = append(out, sessionJSON(s, a.Runs.Get(s.ID)))
	}
	writeJSON(w, 200, out)
}

func sessionJSON(s store.ChatSession, run *Run) map[string]any {
	state := s.State
	if run != nil && run.IsRunning() {
		state = "running"
	}
	return map[string]any{"id": s.ID, "title": s.Title, "model": s.Model, "autonomy": s.Autonomy, "state": state,
		"created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "tokens_in": s.TokensIn, "tokens_out": s.TokensOut,
		"batch": s.BatchID, "repo": s.RepoName, "repo_kind": s.RepoKind, "repo_branch": s.RepoBranch,
		"environment": map[string]string{"image": s.Image, "note": s.EnvNote}, "plan_mode": s.PlanMode, "plan_state": s.PlanState, "plan_text": s.PlanText}
}

// RepoSpec says which repository a session should start from.
type RepoSpec struct {
	Kind     string               `json:"kind"`
	FullName string               `json:"full_name"`
	CloneURL string               `json:"clone_url"`
	Branch   string               `json:"default_branch"`
	Create   *connections.NewRepo `json:"create"`
}

// SessionParams are what a new session needs.
type SessionParams struct {
	Title    string    `json:"title"`
	Model    string    `json:"model"`
	Autonomy string    `json:"autonomy"`
	Repo     *RepoSpec `json:"repo"`
	// Plan makes the agent propose a plan first and change nothing until the user approves it.
	Plan bool `json:"plan"`
	// Template names an approved environment (sandbox image) to run in.
	Template string `json:"template"`
	// batchID is set when the session is one of several attempts started together.
	batchID string
}

// statusError is an error with the HTTP status it should be reported with.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// newSession checks and creates a session for the user: the model must be one they may use, their
// limits must allow it, and any repository must be on their own connection.
func (a *App) newSession(ctx context.Context, u *store.User, b SessionParams) (store.ChatSession, error) {
	if b.Autonomy == "" {
		b.Autonomy = string(agent.Workspace)
	}
	if !agent.ValidAutonomy(agent.Autonomy(b.Autonomy)) {
		return store.ChatSession{}, &statusError{400, "invalid autonomy level"}
	}
	provID, modelName, ok := a.resolveModel(ctx, u, b.Model)
	if !ok {
		return store.ChatSession{}, &statusError{http.StatusForbidden, "that model is not available to you"}
	}
	existing, _ := a.Store.ListChatSessions(ctx, u.ID)
	active := 0
	for _, s := range existing {
		if s.State == "running" || s.State == "needs_input" { // idle sessions hold no running work
			active++
		}
	}
	if err := a.Quota.CheckSandboxStart(ctx, u, active); err != nil {
		return store.ChatSession{}, &statusError{http.StatusTooManyRequests, err.Error()}
	}
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "New session"
	}
	now := a.Now().UTC()
	s := store.ChatSession{ID: newID(), UserID: u.ID, Title: title, ProviderID: provID, Model: modelName, Autonomy: b.Autonomy, State: "idle", CreatedAt: now, UpdatedAt: now, PlanMode: b.Plan, BatchID: b.batchID}
	if b.Repo != nil {
		rp, err := a.resolveRepo(ctx, u.ID, b.Repo.Kind, b.Repo.FullName, b.Repo.CloneURL, b.Repo.Branch, b.Repo.Create)
		if err != nil {
			return store.ChatSession{}, &statusError{http.StatusBadRequest, err.Error()}
		}
		s.RepoKind, s.RepoName, s.RepoURL, s.RepoBase = b.Repo.Kind, rp.FullName, rp.CloneURL, rp.DefaultBranch
		s.RepoBranch = workBranch(s.ID)
		if strings.TrimSpace(b.Title) == "" {
			s.Title = rp.FullName
		}
		if g, ok := a.Connections.Git(b.Repo.Kind); ok {
			if err := a.chooseEnvironment(ctx, &s, a.credsFor(ctx, u.ID, g.Kind()), g, b.Template); err != nil {
				return store.ChatSession{}, &statusError{http.StatusBadRequest, err.Error()}
			}
		}
	} else if b.Template != "" {
		if err := a.chooseEnvironment(ctx, &s, nil, nil, b.Template); err != nil {
			return store.ChatSession{}, &statusError{http.StatusBadRequest, err.Error()}
		}
	}
	if err := a.Store.CreateChatSession(ctx, s); err != nil {
		return store.ChatSession{}, &statusError{500, "could not create session"}
	}
	return s, nil
}

func (a *App) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var b SessionParams
	if !decode(w, r, &b) {
		return
	}
	s, err := a.newSession(r.Context(), a.user(r), b)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			writeErr(w, se.code, se.msg)
		} else {
			writeErr(w, 500, "could not create session")
		}
		return
	}
	writeJSON(w, http.StatusCreated, sessionJSON(s, nil))
}

// resolveModel checks the user may use "provider/model" and returns ids.
func (a *App) resolveModel(ctx context.Context, u *store.User, id string) (int64, string, bool) {
	pn, mn, ok := strings.Cut(id, "/")
	if !ok {
		return 0, "", false
	}
	if len(u.AllowedModels) > 0 {
		found := false
		for _, m := range u.AllowedModels {
			if m == id {
				found = true
			}
		}
		if !found {
			return 0, "", false
		}
	}
	provs, _ := a.Store.ListProviders(ctx)
	for _, p := range provs {
		if p.Name != pn || !p.Enabled {
			continue
		}
		ms, _ := a.Store.ListModels(ctx, p.ID)
		for _, m := range ms {
			if m.Name == mn && m.Enabled {
				return p.ID, mn, true
			}
		}
	}
	return 0, "", false
}

func (a *App) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.ownSession(w, r); ok {
		writeJSON(w, 200, sessionJSON(*s, a.Runs.Get(s.ID)))
	}
}

func (a *App) handleSessionPatch(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Title    *string `json:"title"`
		Autonomy *string `json:"autonomy"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Title != nil && strings.TrimSpace(*b.Title) != "" {
		s.Title = strings.TrimSpace(*b.Title)
	}
	if b.Autonomy != nil {
		if !agent.ValidAutonomy(agent.Autonomy(*b.Autonomy)) {
			writeErr(w, 400, "invalid autonomy level")
			return
		}
		s.Autonomy = *b.Autonomy
	}
	s.UpdatedAt = a.Now().UTC()
	if err := a.Store.UpdateChatSession(r.Context(), *s); err != nil {
		writeErr(w, 500, "could not update")
		return
	}
	writeJSON(w, 200, sessionJSON(*s, a.Runs.Get(s.ID)))
}

func (a *App) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	if run := a.Runs.Get(s.ID); run != nil {
		run.Stop()
	}
	if s.SandboxID != "" && a.Sandbox != nil {
		_ = a.Sandbox.Destroy(r.Context(), s.ID, s.SandboxID)
	}
	if err := a.Store.DeleteChatSession(r.Context(), s.ID); err != nil {
		writeErr(w, 500, "could not delete")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	ms, err := a.Store.ListMessages(r.Context(), s.ID, r.URL.Query().Get("abandoned") == "1")
	if err != nil {
		writeErr(w, 500, "could not load messages")
		return
	}
	out := []map[string]any{}
	for _, m := range ms {
		out = append(out, map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "tool_calls": m.ToolCalls, "tool_call_id": m.ToolCallID, "abandoned": m.Abandoned, "created_at": m.CreatedAt})
	}
	writeJSON(w, 200, out)
}

// launchRun starts the agent on a session with a message. It is what a person's message and an
// automation's run both go through, so the same checks apply to both.
func (a *App) launchRun(ctx context.Context, s *store.ChatSession, u *store.User, text string) error {
	if a.RunAgent == nil {
		return &statusError{http.StatusServiceUnavailable, "the agent is not available"}
	}
	if full, used, limit := a.workspaceFull(ctx, u.ID, s.ID); full {
		return &statusError{http.StatusInsufficientStorage, workspaceFullMessage(used, limit)}
	}
	sc, uc := *s, *u
	a.Terms.CloseSession(s.ID, "The agent started working, so the terminal was closed.")
	if _, started := a.Runs.Start(s.ID, func(ctx context.Context, run *Run) { a.RunAgent(ctx, run, &sc, &uc, text) }); !started {
		return &statusError{http.StatusConflict, "this session is already working; stop it first"}
	}
	return nil
}

func (a *App) startRun(w http.ResponseWriter, r *http.Request, s *store.ChatSession, text string) {
	if err := a.launchRun(r.Context(), s, a.user(r), text); err != nil {
		var se *statusError
		if errors.As(err, &se) {
			writeErr(w, se.code, se.msg)
		} else {
			writeErr(w, 500, "could not start")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
	}
	if !decode(w, r, &b) {
		return
	}
	text := b.Text
	if expanded, c := expandCommand(text, a.commandsFor(r.Context(), s, principal(r).Account.ID)); c != nil {
		text = expanded
	}
	if len(b.Attachments) > 0 {
		items, err := a.checkAttachments(r.Context(), s, b.Attachments)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(text) == "" {
			text = "Please look at the attached file."
			if len(items) > 1 {
				text = "Please look at the attached files."
			}
		}
		text = agent.ComposeAttachments(text, items)
	}
	if strings.TrimSpace(text) == "" || len(text) > 100_000 {
		writeErr(w, 400, "message is empty or too long")
		return
	}
	a.startRun(w, r, s, text)
}

const maxAttachments = 8

// checkAttachments confirms that the named files were uploaded to this session's attachment folder.
func (a *App) checkAttachments(ctx context.Context, s *store.ChatSession, paths []string) ([]agent.Attachment, error) {
	if len(paths) > maxAttachments {
		return nil, fmt.Errorf("attach at most %d files to one message", maxAttachments)
	}
	if s.SandboxID == "" || a.Sandbox == nil {
		return nil, errors.New("upload the files first")
	}
	var items []agent.Attachment
	seen := map[string]bool{}
	for _, p := range paths {
		sp, err := agent.SafePath(p)
		if err != nil || !strings.HasPrefix(sp, agent.AttachmentDir+"/") {
			return nil, errors.New("attachments must be files uploaded with the paperclip")
		}
		if seen[sp] {
			continue
		}
		seen[sp] = true
		res, err := a.Sandbox.Exec(ctx, s.ID, s.SandboxID, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `[ -f "$1" ] && stat -c %s -- "$1"`, "_", sp}, WorkingDir: "/", Timeout: 20 * time.Second})
		if err != nil || res.ExitCode != 0 {
			return nil, fmt.Errorf("%s is not uploaded (any more)", path.Base(sp))
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
		items = append(items, agent.Attachment{Path: sp, Kind: agent.KindOf(sp), Size: n})
	}
	return items, nil
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	if run := a.Runs.Get(s.ID); run != nil {
		run.Stop()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleApproval(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Approve bool `json:"approve"`
	}
	if !decode(w, r, &b) {
		return
	}
	run := a.Runs.Get(s.ID)
	if run == nil || !run.Decide(chi.URLParam(r, "call"), b.Approve) {
		writeErr(w, 404, "no such pending approval")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleAnswer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	if run := a.Runs.Get(s.ID); run == nil || !run.Answer(b.Text) {
		writeErr(w, 409, "the agent is not waiting for an answer")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	run := a.Runs.Get(s.ID)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	if run == nil {
		fmt.Fprint(w, "event: idle\ndata: {}\n\n")
		fl.Flush()
		return
	}
	replay, ch, unsub := run.Subscribe()
	defer unsub()
	send := func(e agent.Event) {
		b, _ := jsonMarshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, b)
		fl.Flush()
	}
	for _, e := range replay {
		send(e)
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-ch:
			send(e)
		case <-run.done:
			// drain anything emitted just before completion
			for {
				select {
				case e := <-ch:
					send(e)
				default:
					fmt.Fprint(w, "event: end\ndata: {}\n\n")
					fl.Flush()
					return
				}
			}
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	ms, _ := a.Store.ListMessages(r.Context(), s.ID, false)
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="session.json"`)
		writeJSON(w, 200, map[string]any{"session": sessionJSON(*s, nil), "messages": ms})
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nModel: %s\n\n", s.Title, s.Model)
	for _, m := range ms {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "## You\n\n%s\n\n", m.Content)
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				fmt.Fprintf(&b, "## Assistant\n\n%s\n\n", m.Content)
			}
			if m.ToolCalls != "" {
				fmt.Fprintf(&b, "```json\n%s\n```\n\n", m.ToolCalls)
			}
		case "tool":
			fmt.Fprintf(&b, "```\n%s\n```\n\n", m.Content)
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="session.md"`)
	_, _ = w.Write([]byte(b.String()))
}

// healthFindings returns the last computed health snapshot.
func (a *App) healthFindings(_ context.Context) []health.Finding {
	a.mu.Lock()
	defer a.mu.Unlock()
	// never nil: the admin app reads the list as JSON, and a null there blanks its health page
	return append([]health.Finding{}, a.healthSnap...)
}

// SetHealth stores the latest findings (called by the health collector).
func (a *App) SetHealth(fs []health.Finding) {
	a.mu.Lock()
	a.healthSnap = fs
	a.mu.Unlock()
}
