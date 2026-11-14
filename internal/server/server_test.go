// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/quota"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
	"github.com/pquerna/otp/totp"
)

const pw = "correct horse battery staple"

type env struct {
	t           *testing.T
	app         *App
	st          *store.SQLite
	mail        *mail.Recorder
	user        *httptest.Server
	admin       *httptest.Server
	now         time.Time
	adminSecret string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, _ := vault.New(bytes.Repeat([]byte{5}, 32))
	rec := &mail.Recorder{}
	cfg := &config.Config{BaseURL: "http://localhost:8080"}
	e := &env{t: t, st: st, mail: rec, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	as := auth.NewService(st, v, rec, cfg.BaseURL)
	as.Now = func() time.Time { return e.now }
	q := quota.New(st, func() quota.Defaults { return quota.Defaults{DailyTokens: 100000} })
	sec := &secrets.Service{Store: st, Vault: v}
	e.app = New(cfg, st, as, q, sec, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.app.Now = func() time.Time { return e.now }
	q.Now = func() time.Time { return e.now }
	e.user = httptest.NewServer(e.app.UserHandler())
	e.admin = httptest.NewServer(e.app.AdminHandler())
	t.Cleanup(e.user.Close)
	t.Cleanup(e.admin.Close)
	return e
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func (e *env) client(base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: base, hc: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) do(method, path string, body any) (int, map[string]any, []byte) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func (c *client) login(path, email, password string) (int, map[string]any) {
	code, m, _ := c.do("POST", path, map[string]string{"email": email, "password": password})
	if s, ok := m["csrf"].(string); ok {
		c.csrf = s
	}
	return code, m
}

var tokRe = regexp.MustCompile(`token=([A-Za-z0-9_%\-]+)`)

func (e *env) invite(email string) string {
	e.t.Helper()
	adm := e.makeAdminSession()
	code, m, raw := adm.do("POST", "/api/users", map[string]string{"email": email, "name": "Test Person"})
	if code != 201 {
		e.t.Fatalf("invite: %d %s", code, raw)
	}
	_ = m
	msg, _ := e.mail.Last()
	g := tokRe.FindStringSubmatch(msg.Body)
	tok, _ := url.QueryUnescape(g[1])
	return tok
}

// makeAdminSession creates (once) an admin with 2FA and returns a logged-in client.
func (e *env) makeAdminSession() *client {
	e.t.Helper()
	ctx := context.Background()
	if n, _ := e.st.CountAccounts(ctx, store.KindAdmin); n == 0 {
		id, err := e.app.Auth.CreateAdmin(ctx, "boss@example.com", "Admin Person", pw)
		if err != nil {
			e.t.Fatal(err)
		}
		a, _ := e.st.GetAccount(ctx, store.KindAdmin, id)
		setup, _ := e.app.Auth.BeginTOTP(ctx, &auth.Principal{Account: a})
		code, _ := totp.GenerateCode(setup.Secret, e.now)
		if _, err := e.app.Auth.ConfirmTOTP(ctx, &auth.Principal{Account: a}, code); err != nil {
			e.t.Fatal(err)
		}
		e.adminSecret = setup.Secret
	}
	e.now = e.now.Add(31 * time.Second)
	c := e.client(e.admin.URL)
	code, m := c.login("/api/login", "boss@example.com", pw)
	if code != 200 || m["needs_mfa"] != true {
		e.t.Fatalf("admin login: %d %v", code, m)
	}
	otp, _ := totp.GenerateCode(e.adminSecret, e.now)
	code, m, _ = c.do("POST", "/api/login/mfa", map[string]string{"code": otp})
	if code != 200 {
		e.t.Fatalf("admin mfa: %d %v", code, m)
	}
	c.csrf, _ = m["csrf"].(string)
	if c.csrf == "" {
		// csrf comes from /me after MFA
		_, me, _ := c.do("GET", "/api/me", nil)
		c.csrf, _ = me["csrf"].(string)
	}
	return c
}

func (e *env) userSession(email string) *client {
	e.t.Helper()
	tok := e.invite(email)
	pub := e.client(e.user.URL)
	if code, m, raw := pub.do("POST", "/api/invite/accept", map[string]string{"token": tok, "password": pw}); code != 200 {
		e.t.Fatalf("accept: %d %v %s", code, m, raw)
	}
	c := e.client(e.user.URL)
	if code, m := c.login("/api/login", email, pw); code != 200 {
		e.t.Fatalf("login: %d %v", code, m)
	}
	if c.csrf == "" {
		_, me, _ := c.do("GET", "/api/me", nil)
		c.csrf, _ = me["csrf"].(string)
	}
	return c
}

func TestMain(m *testing.M) { m.Run() }

func TestNoSelfRegistrationRoute(t *testing.T) {
	e := newEnv(t)
	c := e.client(e.user.URL)
	for _, p := range []string{"/api/register", "/api/signup", "/api/users", "/api/sign-up", "/register", "/signup"} {
		for _, m := range []string{"POST", "PUT", "GET"} {
			code, _, _ := c.do(m, p, map[string]string{"email": "x@example.com", "password": pw})
			if code != 404 && code != 405 && code != 401 {
				t.Errorf("%s %s -> %d", m, p, code)
			}
			if code == 200 || code == 201 {
				t.Fatalf("registration possible via %s %s", m, p)
			}
		}
	}
	if n, _ := e.st.CountAccounts(context.Background(), store.KindUser); n != 0 {
		t.Fatal("a user was created")
	}
}

func TestUserAppHasNoAdminRoutes(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	for _, p := range []string{"/api/providers", "/api/audit", "/api/health", "/api/kill", "/api/settings", "/api/egress/all", "/api/mail/test"} {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			code, _, _ := u.do(m, p, map[string]string{})
			if code == 200 || code == 201 || code == 202 {
				if p == "/api/egress/all" || p == "/api/providers" {
					t.Errorf("chat app answered admin route %s %s with %d", m, p, code)
				}
				if p != "/api/health" && p != "/api/egress" {
					t.Errorf("chat app answered admin route %s %s with %d", m, p, code)
				}
			}
		}
	}
	code, _, _ := u.do("GET", "/api/users", nil)
	if code == 200 {
		t.Fatal("chat user listed users")
	}
}

func TestAdminAndUserSeparation(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	usr := e.userSession("a@example.com")
	// admin credentials do not work on the chat app and vice versa
	if code, _ := e.client(e.user.URL).login("/api/login", "boss@example.com", pw); code != 401 {
		t.Fatalf("admin logged into the chat app: %d", code)
	}
	if code, _ := e.client(e.admin.URL).login("/api/login", "a@example.com", pw); code != 401 {
		t.Fatalf("chat user logged into the admin app: %d", code)
	}
	// admin session cookie replayed against the chat app's authed API
	var adminCookie, userCookie *http.Cookie
	for _, ck := range adm.hc.Jar.Cookies(mustURL(e.admin.URL)) {
		adminCookie = ck
	}
	for _, ck := range usr.hc.Jar.Cookies(mustURL(e.user.URL)) {
		userCookie = ck
	}
	if adminCookie.Name == userCookie.Name {
		t.Fatal("admin and user share a cookie name")
	}
	replay := func(base, name, value, path string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.AddCookie(&http.Cookie{Name: name, Value: value})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := replay(e.user.URL, CookieUser, adminCookie.Value, "/api/sessions"); c != 401 {
		t.Fatalf("admin token accepted as chat session: %d", c)
	}
	if c := replay(e.admin.URL, CookieAdmin, userCookie.Value, "/api/users"); c != 401 {
		t.Fatalf("chat token accepted as admin session: %d", c)
	}
	// an admin cannot open chat sessions (no such route on the admin app)
	if code, _, _ := adm.do("POST", "/api/sessions", map[string]string{"model": "p/m"}); code == 200 || code == 201 {
		t.Fatalf("admin created a chat session: %d", code)
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestCSRFAndOriginEnforced(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	good := u.csrf
	u.csrf = ""
	if code, _, _ := u.do("POST", "/api/sessions", map[string]string{"model": "p/m"}); code != 403 {
		t.Fatalf("missing CSRF token accepted: %d", code)
	}
	u.csrf = "wrong"
	if code, _, _ := u.do("POST", "/api/sessions", map[string]string{"model": "p/m"}); code != 403 {
		t.Fatalf("wrong CSRF token accepted: %d", code)
	}
	u.csrf = good
	req, _ := http.NewRequest("PATCH", e.user.URL+"/api/sessions/x", strings.NewReader(`{}`))
	req.Header.Set("X-CSRF-Token", good)
	req.Header.Set("Origin", "https://evil.example.org")
	for _, ck := range u.hc.Jar.Cookies(mustURL(e.user.URL)) {
		req.AddCookie(ck)
	}
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin request accepted: %d", resp.StatusCode)
	}
	// public POSTs also refuse cross-origin
	req, _ = http.NewRequest("POST", e.user.URL+"/api/login", strings.NewReader(`{"email":"a@example.com","password":"x"}`))
	req.Header.Set("Origin", "https://evil.example.org")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin login accepted: %d", resp.StatusCode)
	}
}

func TestSecurityHeadersAndCookieFlags(t *testing.T) {
	e := newEnv(t)
	e.app.Cfg.BaseURL = "https://syntaxis.example.com"
	resp, _ := http.Get(e.user.URL + "/healthz")
	resp.Body.Close()
	h := resp.Header
	for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Strict-Transport-Security"} {
		if h.Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || strings.Contains(h.Get("Content-Security-Policy"), "unsafe-eval") {
		t.Fatal("csp")
	}
	tok := e.invite("a@example.com")
	_, _, _ = e.client(e.user.URL).do("POST", "/api/invite/accept", map[string]string{"token": tok, "password": pw})
	body, _ := json.Marshal(map[string]string{"email": "a@example.com", "password": pw})
	resp, _ = http.Post(e.user.URL+"/api/login", "application/json", bytes.NewReader(body))
	resp.Body.Close()
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieUser {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", ck)
	}
}

func TestInviteLoginResetFlow(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	code, me, _ := u.do("GET", "/api/me", nil)
	if code != 200 || me["email"] != "a@example.com" || me["remind_2fa"] != true {
		t.Fatalf("%d %v", code, me)
	}
	// generic forgot-password response regardless of existence
	c := e.client(e.user.URL)
	_, a, _ := c.do("POST", "/api/password/forgot", map[string]string{"email": "a@example.com"})
	_, b, _ := c.do("POST", "/api/password/forgot", map[string]string{"email": "ghost@example.com"})
	if a["message"] != b["message"] || a["message"] == nil {
		t.Fatalf("response reveals account existence: %v vs %v", a, b)
	}
	msg, _ := e.mail.Last()
	if msg.To != "a@example.com" {
		t.Fatalf("reset mail %+v", msg)
	}
	g := tokRe.FindStringSubmatch(msg.Body)
	tok, _ := url.QueryUnescape(g[1])
	if code, _, _ := c.do("POST", "/api/password/reset", map[string]string{"token": tok, "password": "a different long passphrase"}); code != 200 {
		t.Fatal("reset failed")
	}
	if code, _, _ := c.do("POST", "/api/password/reset", map[string]string{"token": tok, "password": "a different long passphrase"}); code != 400 {
		t.Fatal("reset token reusable")
	}
	// old session was revoked by the reset
	if code, _, _ := u.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("old session survived reset: %d", code)
	}
}

func TestLockoutOverHTTP(t *testing.T) {
	e := newEnv(t)
	e.userSession("a@example.com")
	c := e.client(e.user.URL)
	var last int
	for i := 0; i < auth.MaxFailures+1; i++ {
		last, _ = c.login("/api/login", "a@example.com", "wrong-password-zz")
	}
	if last != 429 {
		t.Fatalf("lockout status %d", last)
	}
}

func TestAdminRequires2FAForEverything(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.app.Auth.CreateAdmin(ctx, "boss@example.com", "Admin Person", pw); err != nil {
		t.Fatal(err)
	}
	c := e.client(e.admin.URL)
	code, m := c.login("/api/login", "boss@example.com", pw)
	if code != 200 || m["must_setup_2fa"] != true {
		t.Fatalf("%d %v", code, m)
	}
	for _, p := range []string{"/api/users", "/api/providers", "/api/audit", "/api/settings", "/api/health"} {
		if code, _, _ := c.do("GET", p, nil); code != 403 {
			t.Errorf("admin without 2FA reached %s: %d", p, code)
		}
	}
	if code, _, _ := c.do("GET", "/api/me", nil); code != 200 {
		t.Fatal("me must work so the UI can show the setup screen")
	}
	if code, m, _ := c.do("POST", "/api/2fa/begin", nil); code != 200 || m["secret"] == nil {
		t.Fatalf("2fa begin: %d", code)
	}
}

func TestPasswordRotationBlocksAPI(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	e.now = e.now.Add(31 * 24 * time.Hour)
	// sessions expire after 12h; log in again at the later date
	c := e.client(e.user.URL)
	code, m := c.login("/api/login", "a@example.com", pw)
	if code != 200 || m["must_change_password"] != true {
		t.Fatalf("rotation not forced: %v", m)
	}
	if code, _, _ := c.do("GET", "/api/sessions", nil); code != 403 {
		t.Fatalf("API usable with an expired password: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/me", nil); code != 200 {
		t.Fatal("me must remain available")
	}
	if code, _, raw := c.do("POST", "/api/password/change", map[string]string{"current": pw, "new": "a brand new long passphrase"}); code != 200 {
		t.Fatalf("change: %d %s", code, raw)
	}
	_ = u
	c2 := e.client(e.user.URL)
	c2.login("/api/login", "a@example.com", "a brand new long passphrase")
	if code, _, _ := c2.do("GET", "/api/sessions", nil); code != 200 {
		t.Fatalf("after change: %d", code)
	}
}

func seedModel(e *env) {
	ctx := context.Background()
	pid, _ := e.st.UpsertProvider(ctx, store.Provider{Name: "prov", Kind: "ollama", BaseURL: "http://llm.internal:11434", Concurrency: 1, Enabled: true})
	mid, _ := e.st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: "m1", ToolsSupported: 1})
	_ = e.st.SetModelEnabled(ctx, mid, true)
	mid2, _ := e.st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: "m2", ToolsSupported: 1})
	_ = e.st.SetModelEnabled(ctx, mid2, true)
}

func TestModelAccessControl(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("a@example.com")
	_, _, raw := u.do("GET", "/api/models", nil)
	var ms []map[string]any
	_ = json.Unmarshal(raw, &ms)
	if len(ms) != 2 {
		t.Fatalf("%s", raw)
	}
	// admin restricts the user to prov/m1
	adm := e.makeAdminSession()
	users, _ := e.st.ListUsers(context.Background())
	if code, _, raw := adm.do("PATCH", "/api/users/"+itoa(users[0].ID), map[string]any{"allowed_models": []string{"prov/m1"}}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, _, raw = u.do("GET", "/api/models", nil)
	_ = json.Unmarshal(raw, &ms)
	if len(ms) != 1 || ms[0]["id"] != "prov/m1" {
		t.Fatalf("%s", raw)
	}
	if code, _, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m2"}); code != 403 {
		t.Fatalf("session created with a model the user may not use: %d", code)
	}
	if code, _, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/nope"}); code != 403 {
		t.Fatal("nonexistent model")
	}
	if code, _, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"}); code != 201 {
		t.Fatalf("allowed model refused: %d", code)
	}
}

func TestSessionIsolationBetweenUsers(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")
	_, s, _ := alice.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "Alice's secret project"})
	id := s["id"].(string)
	for _, tc := range [][2]string{{"GET", ""}, {"PATCH", ""}, {"DELETE", ""}, {"GET", "/messages"}, {"POST", "/messages"}, {"GET", "/events"}, {"POST", "/stop"}, {"GET", "/export"}, {"POST", "/approvals/x"}, {"POST", "/answer"}} {
		code, _, _ := bob.do(tc[0], "/api/sessions/"+id+tc[1], map[string]any{"text": "hi", "approve": true, "title": "pwned"})
		if code != 404 {
			t.Errorf("bob %s %s -> %d (want 404, no existence leak)", tc[0], tc[1], code)
		}
	}
	_, _, raw := bob.do("GET", "/api/sessions", nil)
	if strings.Contains(string(raw), "Alice") || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("bob sees alice's sessions: %s", raw)
	}
	if code, g, _ := alice.do("GET", "/api/sessions/"+id, nil); code != 200 || g["title"] != "Alice's secret project" {
		t.Fatal("alice lost access")
	}
}

func TestSecretsAPIOwnNamesOnlyNeverValues(t *testing.T) {
	e := newEnv(t)
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")
	if code, _, _ := alice.do("PUT", "/api/secrets/GITHUB_TOKEN", map[string]string{"value": "alice-token-value-123"}); code != 200 {
		t.Fatal("put")
	}
	if code, _, _ := alice.do("PUT", "/api/secrets/bad name", map[string]string{"value": "x"}); code == 200 {
		t.Fatal("bad name accepted")
	}
	_, _, raw := alice.do("GET", "/api/secrets", nil)
	if strings.Contains(string(raw), "alice-token-value") || !strings.Contains(string(raw), "GITHUB_TOKEN") {
		t.Fatalf("%s", raw)
	}
	_, _, raw = bob.do("GET", "/api/secrets", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("bob sees %s", raw)
	}
	if code, _, _ := bob.do("DELETE", "/api/secrets/GITHUB_TOKEN", nil); code != 404 {
		t.Fatalf("bob deleted alice's secret: %d", code)
	}
}

func TestAdminSeesMetadataNotContent(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	alice := e.userSession("alice@example.com")
	_, s, _ := alice.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "private title"})
	id := s["id"].(string)
	_, _ = e.st.AppendMessage(context.Background(), store.Message{SessionID: id, Role: "user", Content: "private content", CreatedAt: e.now})
	adm := e.makeAdminSession()
	_, _, raw := adm.do("GET", "/api/sessions", nil)
	if strings.Contains(string(raw), "private") || !strings.Contains(string(raw), id) {
		t.Fatalf("admin session list: %s", raw)
	}
	if code, _, _ := adm.do("GET", "/api/sessions/"+id+"/messages", nil); code != 403 {
		t.Fatalf("admin read content by default: %d", code)
	}
	e.app.Cfg.AdminReadMsg = true
	if code, _, raw := adm.do("GET", "/api/sessions/"+id+"/messages", nil); code != 200 || !strings.Contains(string(raw), "private content") {
		t.Fatalf("opt-in read: %d", code)
	}
	es, _ := e.st.ListAudit(context.Background(), 0)
	found := false
	for _, a := range es {
		if a.Action == "admin.read_content" {
			found = true
		}
	}
	if !found {
		t.Fatal("admin content access must be audited")
	}
}

func TestEgressLogVisibility(t *testing.T) {
	e := newEnv(t)
	alice := e.userSession("alice@example.com")
	e.userSession("bob@example.com")
	ctx := context.Background()
	users, _ := e.st.ListUsers(ctx)
	for _, u := range users {
		_ = e.st.AddEgress(ctx, store.EgressEntry{Time: e.now, UserID: u.ID, SessionID: "s", DestHost: "h" + itoa(u.ID) + ".example.com", Port: 443, Protocol: "tcp", Allowed: true})
	}
	_, _, raw := alice.do("GET", "/api/egress", nil)
	var mine []map[string]any
	_ = json.Unmarshal(raw, &mine)
	if len(mine) != 1 {
		t.Fatalf("user sees %d entries: %s", len(mine), raw)
	}
	adm := e.makeAdminSession()
	_, _, raw = adm.do("GET", "/api/egress", nil)
	var all []map[string]any
	_ = json.Unmarshal(raw, &all)
	if len(all) != 2 {
		t.Fatalf("admin sees %d", len(all))
	}
}

func TestAdminUserManagementAndAudit(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	if code, _, _ := adm.do("POST", "/api/users", map[string]string{"email": "bad", "name": "x"}); code != 400 {
		t.Fatal("bad email")
	}
	e.invite("a@example.com")
	if code, _, _ := adm.do("POST", "/api/users", map[string]string{"email": "a@example.com", "name": "x"}); code != 409 {
		t.Fatalf("duplicate: %d", code)
	}
	users, _ := e.st.ListUsers(context.Background())
	id := itoa(users[0].ID)
	if code, _, _ := adm.do("PATCH", "/api/users/"+id, map[string]any{"egress_mode": "wide"}); code != 400 {
		t.Fatal("invalid egress mode accepted")
	}
	if code, _, _ := adm.do("PATCH", "/api/users/"+id, map[string]any{"quota_daily_tokens": 500, "egress_mode": "none"}); code != 200 {
		t.Fatal("patch")
	}
	_, _, raw := adm.do("GET", "/api/users", nil)
	if strings.Contains(string(raw), "password") {
		t.Fatalf("admin user list leaks password data: %s", raw)
	}
	_, a, _ := adm.do("GET", "/api/audit", nil)
	if a["chain_ok"] != true {
		t.Fatalf("%v", a)
	}
	// admin tampering with the DB is detectable through the endpoint
	if code, _, _ := adm.do("PUT", "/api/settings", map[string]string{"password_rotation_days": "oops"}); code != 400 {
		t.Fatal("invalid setting accepted")
	}
	if code, _, _ := adm.do("PUT", "/api/settings", map[string]string{"password_rotation_days": "14", "secret_scan_mode": "block"}); code != 200 {
		t.Fatal("settings")
	}
	if v, _ := e.st.GetSetting(context.Background(), SettingRotationDays, ""); v != "14" {
		t.Fatal("not stored")
	}
}

func TestDisablingUserRevokesSessions(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	adm := e.makeAdminSession()
	users, _ := e.st.ListUsers(context.Background())
	adm.do("PATCH", "/api/users/"+itoa(users[0].ID), map[string]any{"disabled": true})
	if code, _, _ := u.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("disabled user's session still valid: %d", code)
	}
}

func TestProviderKeysEncryptedAndNeverReturned(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	code, m, raw := adm.do("POST", "/api/providers", map[string]any{"name": "p", "kind": "openai", "base_url": "http://llm.internal:8080", "api_key": "sk-secret-provider-key", "concurrency": 2})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, _, raw = adm.do("GET", "/api/providers", nil)
	if strings.Contains(string(raw), "sk-secret") || !strings.Contains(string(raw), `"has_key":true`) {
		t.Fatalf("%s", raw)
	}
	ps, _ := e.st.ListProviders(context.Background())
	if bytes.Contains(ps[0].APIKeyEnc, []byte("sk-secret")) {
		t.Fatal("api key stored in plaintext")
	}
	if code, _, _ := adm.do("POST", "/api/providers", map[string]any{"name": "q", "kind": "bogus", "base_url": "x"}); code != 400 {
		t.Fatal("bad kind")
	}
	_ = m
}

func TestProviderRefreshDiscoversModelsReadOnly(t *testing.T) {
	e := newEnv(t)
	var paths []string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"alpha","digest":"d"},{"name":"beta","digest":"e"}]}`)
		case "/api/show":
			io.WriteString(w, `{"capabilities":["tools"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer llm.Close()
	adm := e.makeAdminSession()
	_, m, _ := adm.do("POST", "/api/providers", map[string]any{"name": "p", "kind": "ollama", "base_url": llm.URL})
	id := itoa(int64(m["id"].(float64)))
	code, r, _ := adm.do("POST", "/api/providers/"+id+"/refresh", nil)
	if code != 200 || r["models"] != float64(2) {
		t.Fatalf("%d %v", code, r)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "DELETE") || strings.Contains(p, "/api/pull") || strings.Contains(p, "/api/delete") {
			t.Fatalf("Syntaxis must be read-only toward the LLM server, saw %s", p)
		}
	}
	_, _, raw := adm.do("GET", "/api/models", nil)
	if !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatalf("discovered models must start disabled: %s", raw)
	}
}

func TestRunContinuesAfterClientDisconnectAndStreams(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	release := make(chan struct{})
	finished := make(chan struct{})
	e.app.RunAgent = func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, msg string) {
		run.Emit(agent.Event{Kind: agent.EvStatus, Data: map[string]any{"status": "working", "msg": msg}})
		<-release
		run.Emit(agent.Event{Kind: agent.EvAssistant, Data: map[string]any{"text": "all done"}})
		close(finished)
	}
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "build it"}); code != 202 {
		t.Fatalf("start: %d", code)
	}
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "again"}); code != 409 {
		t.Fatalf("concurrent runs allowed: %d", code)
	}
	// open the stream, read the first event, then disconnect the browser
	req, _ := http.NewRequest("GET", e.user.URL+"/api/sessions/"+id+"/events", nil)
	for _, ck := range u.hc.Jar.Cookies(mustURL(e.user.URL)) {
		req.AddCookie(ck)
	}
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	seen := ""
	for sc.Scan() {
		seen += sc.Text() + "\n"
		if strings.Contains(seen, "working") {
			break
		}
	}
	cancel() // browser closed
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if _, st, _ := u.do("GET", "/api/sessions/"+id, nil); st["state"] != "running" {
		t.Fatalf("run stopped when the browser closed: %v", st)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not complete in the background")
	}
	// a late subscriber gets the replay including the final message
	req2, _ := http.NewRequest("GET", e.user.URL+"/api/sessions/"+id+"/events", nil)
	for _, ck := range u.hc.Jar.Cookies(mustURL(e.user.URL)) {
		req2.AddCookie(ck)
	}
	resp2, _ := http.DefaultClient.Do(req2)
	b, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(b), "all done") || !strings.Contains(string(b), "event: end") {
		t.Fatalf("replay: %s", b)
	}
}

func TestApprovalAndStopEndpoints(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	decided := make(chan bool, 1)
	e.app.RunAgent = func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, msg string) {
		ok, err := run.Approve(ctx, "call-1")
		if err != nil {
			return
		}
		decided <- ok
	}
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "x"})
	var code int
	for i := 0; i < 50; i++ {
		code, _, _ = u.do("POST", "/api/sessions/"+id+"/approvals/call-1", map[string]bool{"approve": true})
		if code == 200 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code != 200 || !<-decided {
		t.Fatal("approval not delivered")
	}
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/approvals/nope", map[string]bool{"approve": true}); code != 404 {
		t.Fatal("unknown approval")
	}
	// stop cancels a blocked run
	e.app.RunAgent = func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, msg string) { <-ctx.Done() }
	time.Sleep(20 * time.Millisecond)
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "y"}); code != 202 {
		t.Fatalf("restart: %d", code)
	}
	u.do("POST", "/api/sessions/"+id+"/stop", nil)
	e.app.Runs.Get(id).Wait()
}

func TestExportMarkdownAndJSON(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "T"})
	id := s["id"].(string)
	ctx := context.Background()
	_, _ = e.st.AppendMessage(ctx, store.Message{SessionID: id, Role: "user", Content: "hello", CreatedAt: e.now})
	_, _ = e.st.AppendMessage(ctx, store.Message{SessionID: id, Role: "assistant", Content: "hi there", CreatedAt: e.now})
	_, _, md := u.do("GET", "/api/sessions/"+id+"/export", nil)
	if !strings.Contains(string(md), "## You") || !strings.Contains(string(md), "hi there") {
		t.Fatalf("%s", md)
	}
	_, _, js := u.do("GET", "/api/sessions/"+id+"/export?format=json", nil)
	if !json.Valid(js) {
		t.Fatal("json export")
	}
}

func TestUsageEndpoint(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	users, _ := e.st.ListUsers(context.Background())
	_ = e.st.AddUsage(context.Background(), users[0].ID, "2026-11-01", 400, 100, 0)
	code, m, _ := u.do("GET", "/api/usage", nil)
	d, _ := m["daily"].(map[string]any)
	if code != 200 || d["Used"] != float64(500) || d["Limit"] != float64(100000) {
		t.Fatalf("%d %v", code, m)
	}
}

func TestResourcesEndpointIsOwnerOnlyAndSafeWithoutMonitor(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id, _ := s["id"].(string)
	if code, m, _ := u.do("GET", "/api/sessions/"+id+"/resources", nil); code != 200 || m["active"] != false {
		t.Fatalf("%d %v", code, m)
	}
	if code, _, _ := other.do("GET", "/api/sessions/"+id+"/resources", nil); code != 404 {
		t.Fatalf("another user must get 404, got %d", code)
	}
}

func TestMeIncludesHealthBannersForUsersOnly(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("a@example.com")
	e.app.SetHealth(healthFindingForTest())
	_, me, _ := u.do("GET", "/api/me", nil)
	b, _ := me["banners"].([]any)
	if len(b) != 1 {
		t.Fatalf("%v", me)
	}
	adm := e.makeAdminSession()
	code, h, _ := adm.do("GET", "/api/health", nil)
	if code != 200 || h["summary"] == nil {
		t.Fatal("admin health")
	}
	if code, _, _ := adm.do("POST", "/api/health/snooze", map[string]any{"check_id": "provider.unreachable", "subject": "", "hours": 1}); code != 400 {
		t.Fatalf("snoozing an error must be refused: %d", code)
	}
}

func TestVersionIsPublicOnBothApps(t *testing.T) {
	e := newEnv(t)
	for _, base := range []string{e.user.URL, e.admin.URL} {
		c := e.client(base)
		code, m, _ := c.do("GET", "/api/version", nil)
		if code != 200 || m["version"] == nil || m["go"] == nil {
			t.Fatalf("%s: %d %v", base, code, m)
		}
		if code, m, _ := c.do("GET", "/healthz", nil); code != 200 || m["version"] == nil {
			t.Fatalf("healthz must carry the version: %d %v", code, m)
		}
	}
}
