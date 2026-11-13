// SPDX-License-Identifier: GPL-2.0-or-later

// Package server wires everything into two HTTP handlers: the chat-user app
// and the admin app. They share no routes, cookies or credentials.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/lifecycle"
	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/mcp"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/quota"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/signing"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Cookie names differ per account kind so a cookie can never be replayed
// across the two apps.
const (
	CookieUser  = "syx_user"
	CookieAdmin = "syx_admin"
)

// App holds shared dependencies.
type App struct {
	Cfg         *config.Config
	Store       store.Store
	Auth        *auth.Service
	Quota       *quota.Service
	Secrets     *secrets.Service
	Signing     *signing.Service
	Mail        mail.Mailer
	Health      *health.Engine
	Sandbox     *sandbox.Manager // may be nil (sandboxes unavailable)
	Log         *slog.Logger
	Runs        *Runs
	Web         http.Handler // embedded frontend (may be nil)
	Now         func() time.Time
	RunAgent    RunFunc
	Queues      *Queues
	MCPSource   mcp.Source
	Lifecycle   *lifecycle.Job
	AdminWeb    http.Handler
	NewProvider func(p *store.Provider) (provider.Provider, error)

	mu         sync.Mutex
	healthSnap []health.Finding
}

// New builds an App; callers set optional fields afterwards.
func New(cfg *config.Config, st store.Store, as *auth.Service, q *quota.Service, sec *secrets.Service, m mail.Mailer, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	a := &App{Cfg: cfg, Store: st, Auth: as, Quota: q, Secrets: sec, Mail: m, Log: log, Health: health.NewEngine(health.DefaultThresholds()), Now: time.Now}
	a.Runs = NewRuns()
	return a
}

// ---- helpers ----

type ctxKey int

const keyPrincipal ctxKey = 1

func principal(r *http.Request) *auth.Principal {
	p, _ := r.Context().Value(keyPrincipal).(*auth.Principal)
	return p
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func clientIP(r *http.Request, trusted []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// X-Forwarded-For is honoured only from explicitly trusted proxies
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && isTrusted(host, trusted) {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return host
}

func isTrusted(host string, trusted []string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, t := range trusted {
		if p, err := netip.ParsePrefix(t); err == nil && p.Contains(ip) {
			return true
		}
		if a, err := netip.ParseAddr(t); err == nil && a == ip {
			return true
		}
	}
	return false
}

func (a *App) secureCookie() bool { return strings.HasPrefix(a.Cfg.BaseURL, "https://") }

func (a *App) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.secureCookie(), SameSite: http.SameSiteStrictMode,
		Expires: a.Now().Add(ttl), MaxAge: int(ttl.Seconds())})
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookie(), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// ---- middleware ----

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func (a *App) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if a.secureCookie() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		a.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// sameOrigin rejects cross-origin state-changing requests (strict same-origin).
func (a *App) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser clients; CSRF token still required
	}
	return strings.TrimRight(o, "/") == "http://"+r.Host || strings.TrimRight(o, "/") == "https://"+r.Host
}

func isSafe(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// requireAuth authenticates the cookie for kind, enforces CSRF on unsafe methods.
func (a *App) requireAuth(kind string) func(http.Handler) http.Handler {
	cookie := CookieUser
	if kind == store.KindAdmin {
		cookie = CookieAdmin
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookie)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "not signed in")
				return
			}
			p, err := a.Auth.Authenticate(r.Context(), kind, c.Value)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "not signed in")
				return
			}
			if !isSafe(r.Method) {
				if !a.sameOrigin(r) || !auth.CheckCSRF(p.Session, r.Header.Get("X-CSRF-Token")) {
					writeErr(w, http.StatusForbidden, "invalid CSRF token")
					return
				}
			}
			ctx := context.WithValue(r.Context(), keyPrincipal, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// publicPost guards unauthenticated state-changing endpoints with the same-origin check.
func (a *App) publicPost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		next(w, r)
	}
}

// ---- shared auth endpoints (parameterised by kind) ----

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) cookieFor(kind string) string {
	if kind == store.KindAdmin {
		return CookieAdmin
	}
	return CookieUser
}

func (a *App) handleLogin(kind string) http.HandlerFunc {
	return a.publicPost(func(w http.ResponseWriter, r *http.Request) {
		var b loginBody
		if !decode(w, r, &b) {
			return
		}
		res, err := a.Auth.Login(r.Context(), kind, b.Email, b.Password, clientIP(r, a.Cfg.ProxyAuth.Trusted))
		switch {
		case errors.Is(err, auth.ErrLocked):
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		case err != nil:
			writeErr(w, http.StatusUnauthorized, auth.ErrInvalidCredentials.Error())
			return
		}
		ttl := auth.SessionTTL
		if res.NeedsMFA {
			ttl = auth.PendingMFATTL
		}
		a.setCookie(w, a.cookieFor(kind), res.SessionToken, ttl)
		writeJSON(w, http.StatusOK, a.loginJSON(r, res))
	})
}

func (a *App) loginJSON(r *http.Request, res *auth.LoginResult) map[string]any {
	return map[string]any{
		"needs_mfa": res.NeedsMFA, "must_change_password": res.MustChangePassword, "must_setup_2fa": res.MustSetup2FA,
		"remind_2fa": res.Remind2FA, "password_days_left": res.PasswordDaysLeft, "csrf": res.CSRF,
		"name": res.Account.Name,
	}
}

func (a *App) handleMFA(kind string) http.HandlerFunc {
	return a.publicPost(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &b) {
			return
		}
		c, err := r.Cookie(a.cookieFor(kind))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		res, err := a.Auth.VerifyMFA(r.Context(), kind, c.Value, b.Code, clientIP(r, a.Cfg.ProxyAuth.Trusted))
		switch {
		case errors.Is(err, auth.ErrLocked):
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		case err != nil:
			writeErr(w, http.StatusUnauthorized, "invalid code")
			return
		}
		a.setCookie(w, a.cookieFor(kind), res.SessionToken, auth.SessionTTL)
		must, left := a.Auth.RotationStatus(r.Context(), res.Account)
		out := a.loginJSON(r, res)
		out["must_change_password"], out["password_days_left"] = must, left
		writeJSON(w, http.StatusOK, out)
	})
}

func (a *App) handleLogout(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(a.cookieFor(kind)); err == nil {
			_ = a.Auth.Logout(r.Context(), c.Value)
		}
		a.clearCookie(w, a.cookieFor(kind))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (a *App) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	_ = a.Auth.LogoutEverywhere(r.Context(), p.Account.Kind, p.Account.ID)
	a.clearCookie(w, a.cookieFor(p.Account.Kind))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handle2FABegin(w http.ResponseWriter, r *http.Request) {
	s, err := a.Auth.BeginTOTP(r.Context(), principal(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": s.Secret, "url": s.URL})
}

func (a *App) handle2FAConfirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &b) {
		return
	}
	codes, err := a.Auth.ConfirmTOTP(r.Context(), principal(r), b.Code)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &b) {
		return
	}
	p := principal(r)
	if err := a.Auth.ChangePassword(r.Context(), p, b.Current, b.New); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, auth.ErrInvalidCredentials) {
			code = http.StatusUnauthorized
		}
		writeErr(w, code, err.Error())
		return
	}
	a.clearCookie(w, a.cookieFor(p.Account.Kind)) // all sessions were revoked; sign in again
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func chiParam(r *http.Request, k string) string { return chi.URLParam(r, k) }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// decodeOptional decodes a body if present; empty bodies are fine.
func decodeOptional(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}
