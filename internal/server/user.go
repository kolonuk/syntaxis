// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })

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
			r.Get("/secrets", a.handleSecretNames)
			r.Put("/secrets/{name}", a.handleSecretPut)
			r.Delete("/secrets/{name}", a.handleSecretDelete)
			r.Post("/workspace/keep", a.handleWorkspaceKeep)
			r.Get("/sessions", a.handleSessions)
			r.Post("/sessions", a.handleSessionCreate)
			r.Route("/sessions/{id}", func(r chi.Router) {
				r.Get("/", a.handleSessionGet)
				r.Patch("/", a.handleSessionPatch)
				r.Delete("/", a.handleSessionDelete)
				r.Get("/messages", a.handleMessages)
				r.Post("/messages", a.handleSend)
				r.Get("/events", a.handleEvents)
				r.Post("/stop", a.handleStop)
				r.Post("/approvals/{call}", a.handleApproval)
				r.Post("/answer", a.handleAnswer)
				r.Get("/export", a.handleExport)
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
	if p.Account.Kind == store.KindUser {
		out["banners"] = health.UserBanners(a.healthFindings(r.Context()))
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
	return u
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
	var out []map[string]any
	for _, m := range models {
		pn, ok := pname[m.ProviderID]
		if !ok || !m.Enabled {
			continue
		}
		id := pn + "/" + m.Name
		if len(allowed) > 0 && !allowed[id] {
			continue // empty allow-list = every enabled model
		}
		out = append(out, map[string]any{"id": id, "name": m.Name, "provider": pn, "tools": m.ToolsSupported != 0, "recommended": m.Recommended})
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
		"daily": m.Daily, "monthly": m.Monthly, "sandbox_seconds": m.SandboxSeconds,
		"daily_resets_at": m.DailyResetsAt, "monthly_resets_at": m.MonthlyResetsAt,
	})
}

func (a *App) handleMyEgress(w http.ResponseWriter, r *http.Request) {
	es, err := a.Store.ListEgress(r.Context(), principal(r).Account.ID, 500) // only the caller's own
	if err != nil {
		writeErr(w, 500, "could not load egress log")
		return
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
	if !decode(w, r, &b) || b.Value == "" {
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
		"created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "tokens_in": s.TokensIn, "tokens_out": s.TokensOut}
}

func (a *App) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title    string `json:"title"`
		Model    string `json:"model"`
		Autonomy string `json:"autonomy"`
	}
	if !decode(w, r, &b) {
		return
	}
	u := a.user(r)
	if b.Autonomy == "" {
		b.Autonomy = string(agent.Workspace)
	}
	if !agent.ValidAutonomy(agent.Autonomy(b.Autonomy)) {
		writeErr(w, 400, "invalid autonomy level")
		return
	}
	provID, modelName, ok := a.resolveModel(r.Context(), u, b.Model)
	if !ok {
		writeErr(w, http.StatusForbidden, "that model is not available to you")
		return
	}
	existing, _ := a.Store.ListChatSessions(r.Context(), u.ID)
	active := 0
	for _, s := range existing {
		if s.State == "running" || s.State == "idle" || s.State == "needs_input" {
			active++
		}
	}
	if err := a.Quota.CheckSandboxStart(r.Context(), u, active); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "New session"
	}
	now := a.Now().UTC()
	s := store.ChatSession{ID: newID(), UserID: u.ID, Title: title, ProviderID: provID, Model: modelName, Autonomy: b.Autonomy, State: "idle", CreatedAt: now, UpdatedAt: now}
	if err := a.Store.CreateChatSession(r.Context(), s); err != nil {
		writeErr(w, 500, "could not create session")
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

// RunAgent is injected by main; nil means the agent is unavailable.
func (a *App) startRun(w http.ResponseWriter, r *http.Request, s *store.ChatSession, text string) {
	if a.RunAgent == nil {
		writeErr(w, http.StatusServiceUnavailable, "the agent is not available")
		return
	}
	u := a.user(r)
	sc, uc := *s, *u
	run, started := a.Runs.Start(s.ID, func(ctx context.Context, run *Run) { a.RunAgent(ctx, run, &sc, &uc, text) })
	if !started {
		writeErr(w, http.StatusConflict, "this session is already working; stop it first")
		return
	}
	_ = run
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
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
	if strings.TrimSpace(b.Text) == "" || len(b.Text) > 100_000 {
		writeErr(w, 400, "message is empty or too long")
		return
	}
	a.startRun(w, r, s, b.Text)
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
	return append([]health.Finding(nil), a.healthSnap...)
}

// SetHealth stores the latest findings (called by the health collector).
func (a *App) SetHealth(fs []health.Finding) {
	a.mu.Lock()
	a.healthSnap = fs
	a.mu.Unlock()
}
