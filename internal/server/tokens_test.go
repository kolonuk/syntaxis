// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kolonuk/syntaxis/internal/store"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// asToken is a client that sends only the token: no cookie, no CSRF header.
func (e *env) asToken(base, token string) *client {
	return &client{t: e.t, base: base, hc: &http.Client{Transport: bearerTransport{token}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func makeToken(t *testing.T, u *client, name, scope string, days int) (string, map[string]any) {
	t.Helper()
	code, b, raw := u.do("POST", "/api/tokens", map[string]any{"name": name, "scope": scope, "expires_days": days})
	if code != 201 {
		t.Fatalf("create token: %d %s", code, raw)
	}
	tok, _ := b["token"].(string)
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) < 40 {
		t.Fatalf("token %q", tok)
	}
	return tok, b
}

func TestTokenScopes(t *testing.T) {
	for _, c := range []struct {
		scope, method, path string
		want                bool
	}{
		{ScopeRead, "GET", "/api/sessions", true},
		{ScopeRead, "GET", "/api/sessions/abc123/messages", true},
		{ScopeRead, "GET", "/api/sessions/abc123/checkpoints/4/diff", true},
		{ScopeRead, "HEAD", "/api/sessions/abc123/files", true},
		{ScopeRead, "POST", "/api/sessions", false},
		{ScopeRead, "POST", "/api/sessions/abc123/messages", false},
		{ScopeRun, "POST", "/api/sessions", true},
		{ScopeRun, "POST", "/api/sessions/abc123/messages", true},
		{ScopeRun, "POST", "/api/sessions/abc123/approvals/call1", true},
		{ScopeRun, "GET", "/api/sessions/abc123/events", true},
		{ScopeRun, "DELETE", "/api/sessions/abc123", false},
		{ScopeRun, "PUT", "/api/sessions/abc123/files/content", false},
		{ScopeRun, "POST", "/api/sessions/abc123/files/upload", false},
		{ScopeRun, "GET", "/api/sessions/abc123/terminal", false},
		{ScopeRun, "GET", "/api/secrets", false},
		{ScopeRun, "PUT", "/api/hooks", false},
		{ScopeRun, "POST", "/api/tokens", false},
		{ScopeRun, "GET", "/api/sessions/abc/../../secrets", false},
		{ScopeRun, "GET", "/api/sessions/", false},
		{"", "GET", "/api/sessions", false},
		{"admin", "GET", "/api/sessions", false},
	} {
		if got := tokenAllowed(c.scope, c.method, c.path); got != c.want {
			t.Errorf("%s %s %s: %v, want %v", c.scope, c.method, c.path, got, c.want)
		}
	}
}

func TestAPITokensEndToEnd(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	var sent msgLog
	e.app.RunAgent = func(_ context.Context, _ *Run, _ *store.ChatSession, _ *store.User, msg string) {
		sent.add(msg)
	}
	u := e.userSession("tok@example.com")
	other := e.userSession("tok2@example.com")

	read, readInfo := makeToken(t, u, "ci reader", ScopeRead, 0)
	run, runInfo := makeToken(t, u, "builder", ScopeRun, 30)
	// the token is shown once; the list never has it or its hash
	_, _, list := u.do("GET", "/api/tokens", nil)
	if strings.Contains(string(list), read) || strings.Contains(string(list), run) || strings.Contains(string(list), "hash") || !strings.Contains(string(list), "ci reader") {
		t.Fatalf("the list: %s", list)
	}
	if !strings.HasPrefix(read, readInfo["prefix"].(string)) {
		t.Error("the prefix should recognise the token")
	}
	// the database holds only a hash
	if tk, err := e.st.GetAPITokenByHash(context.Background(), read); err == nil {
		t.Fatalf("the plain token is a database key: %+v", tk)
	}
	// bad input
	for _, b := range []map[string]any{{"name": "", "scope": "read"}, {"name": "x", "scope": "admin"}, {"name": "x", "scope": "run", "expires_days": -1}, {"name": "x", "scope": "run", "expires_days": 99999}} {
		if code, _, _ := u.do("POST", "/api/tokens", b); code != 400 {
			t.Errorf("%v: %d", b, code)
		}
	}

	rd, rn := e.asToken(e.user.URL, read), e.asToken(e.user.URL, run)
	// a read token reads and cannot start work
	if code, me, _ := rd.do("GET", "/api/me", nil); code != 200 || me["email"] != "tok@example.com" || me["api_token"].(map[string]any)["scope"] != "read" {
		t.Fatalf("me: %d %v", code, me)
	}
	if code, _, _ := rd.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"}); code != 403 {
		t.Fatalf("a read token started a session: %d", code)
	}
	// a run token starts a session and sends a message, with no CSRF header
	code, s, raw := rn.do("POST", "/api/sessions", map[string]string{"model": "prov/m1", "title": "from a token"})
	if code != 201 && code != 200 {
		t.Fatalf("create: %d %s", code, raw)
	}
	id := s["id"].(string)
	if code, _, raw := rn.do("POST", "/api/sessions/"+id+"/messages", map[string]any{"text": "hello"}); code != 202 {
		t.Fatalf("send: %d %s", code, raw)
	}
	waitFor(t, func() bool { return sent.n() == 1 })
	if sent.at(0) != "hello" {
		t.Fatalf("the agent got %q", sent.at(0))
	}
	if code, b, raw := rd.do("GET", "/api/sessions/"+id, nil); code != 200 || b["title"] != "from a token" {
		t.Fatalf("read back: %d %s", code, raw)
	}
	if code, _, raw := rd.do("GET", "/api/sessions/"+id+"/messages", nil); code != 200 {
		t.Fatalf("messages: %d %s", code, raw)
	}
	// it acts as that user only
	if code, _, _ := other.do("GET", "/api/sessions/"+id, nil); code != 404 {
		t.Fatalf("another user saw the session: %d", code)
	}
	// everything that is not on the list is refused, even for the widest scope
	for _, c := range [][2]string{
		{"GET", "/api/tokens"}, {"POST", "/api/tokens"}, {"DELETE", "/api/tokens/1"}, {"GET", "/api/secrets"}, {"PUT", "/api/secrets/X"},
		{"PUT", "/api/hooks"}, {"PUT", "/api/commands"}, {"GET", "/api/connections"}, {"GET", "/api/automations"}, {"POST", "/api/automations"},
		{"GET", "/api/shares"}, {"POST", "/api/sessions/" + id + "/shares"}, {"GET", "/api/sessions/" + id + "/terminal"},
		{"PUT", "/api/sessions/" + id + "/files/content"}, {"POST", "/api/sessions/" + id + "/files/upload"}, {"DELETE", "/api/sessions/" + id + "/files"},
		{"POST", "/api/sessions/" + id + "/repo-hooks/trust"}, {"DELETE", "/api/sessions/" + id}, {"POST", "/api/2fa/disable"},
		{"POST", "/api/password/change"}, {"POST", "/api/logout-all"}, {"PUT", "/api/mcp"}, {"POST", "/api/memory"},
	} {
		if code, _, _ := rn.do(c[0], c[1], map[string]string{}); code != 403 {
			t.Errorf("a run token reached %s %s: %d", c[0], c[1], code)
		}
	}
	// a token is for the chat app only
	if code, _, _ := e.asToken(e.admin.URL, run).do("GET", "/api/me", nil); code != 401 {
		t.Errorf("a token worked on the admin app: %d", code)
	}
	// a made-up or wrongly prefixed token is not accepted
	for _, bad := range []string{"syx_" + strings.Repeat("a", 43), "nope", strings.TrimPrefix(run, TokenPrefix)} {
		if code, _, _ := e.asToken(e.user.URL, bad).do("GET", "/api/me", nil); code != 401 {
			t.Errorf("token %q: %d", bad, code)
		}
	}
	// what a token changed is in the audit log under the token, not the user
	es, _ := e.st.ListAudit(context.Background(), 0)
	var created, requests int
	for _, a := range es {
		if a.Action == "token.create" {
			created++
		}
		if a.Action == "api.request" && strings.HasPrefix(a.Actor, "token:") {
			requests++
		}
	}
	if created != 2 || requests < 2 {
		t.Errorf("audit: %d creations, %d token requests", created, requests)
	}
	// another user cannot revoke it; the owner can, and it stops working at once
	if code, _, _ := other.do("DELETE", "/api/tokens/"+itoa(int64(runInfo["id"].(float64))), nil); code != 404 {
		t.Fatalf("another user revoked a token: %d", code)
	}
	if code, _, _ := u.do("DELETE", "/api/tokens/"+itoa(int64(runInfo["id"].(float64))), nil); code != 200 {
		t.Fatal("revoke")
	}
	if code, _, _ := rn.do("GET", "/api/me", nil); code != 401 {
		t.Errorf("a revoked token still works: %d", code)
	}
	// a disabled user's tokens stop working
	uid := userID(t, e, "tok@example.com")
	acct, _ := e.st.GetAccount(context.Background(), store.KindUser, uid)
	acct.Disabled = true
	_ = e.st.UpdateAccount(context.Background(), *acct)
	if code, _, _ := rd.do("GET", "/api/me", nil); code != 401 {
		t.Errorf("a disabled user's token works: %d", code)
	}
}

func TestAPITokenExpiresAndIsLimited(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("exp@example.com")
	tok, _ := makeToken(t, u, "short", ScopeRead, 1)
	c := e.asToken(e.user.URL, tok)
	if code, _, _ := c.do("GET", "/api/me", nil); code != 200 {
		t.Fatalf("fresh token: %d", code)
	}
	for i := 1; i < maxAPITokens; i++ {
		makeToken(t, u, "t", ScopeRead, 0)
	}
	if code, _, _ := u.do("POST", "/api/tokens", map[string]any{"name": "one too many", "scope": "read"}); code != 409 {
		t.Errorf("token limit: %d", code)
	}
	e.now = e.now.Add(25 * time.Hour)
	if code, _, _ := c.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("an expired token works: %d", code)
	}
}

// Every route behind sign-in must either be on the token list or be refused for a token. A new route
// is therefore closed to tokens until someone adds it to tokenRules on purpose.
func TestNewRoutesAreClosedToTokensByDefault(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("walk@example.com")
	run, _ := makeToken(t, u, "walker", ScopeRun, 0)
	rn := e.asToken(e.user.URL, run)
	routes, ok := e.app.UserHandler().(chi.Routes)
	if !ok {
		t.Skip("NOT TESTED: the user handler is not a chi router")
	}
	checked := 0
	_ = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/api/login") || method == "CONNECT" || method == "TRACE" {
			return nil
		}
		p := strings.NewReplacer("{id}", "1", "{call}", "c1", "{seq}", "1", "{name}", "X", "{kind}", "github", "{batch}", "b1").Replace(route)
		// only routes that need a sign-in
		anon := e.client(e.user.URL)
		if code, b, _ := anon.do(method, p, map[string]string{}); code != 401 || b["error"] != "not signed in" {
			return nil
		}
		checked++
		code, b, _ := rn.do(method, p, map[string]string{})
		refusedForScope := code == 403 && strings.Contains(fmt.Sprint(b["error"]), "scope")
		if allowed := tokenAllowed(ScopeRun, method, p); !allowed && !refusedForScope {
			t.Errorf("%s %s is not on the token list but a token got %d", method, route, code)
		} else if allowed && refusedForScope {
			t.Errorf("%s %s is on the token list but was refused", method, route)
		}
		return nil
	})
	if checked < 40 {
		t.Fatalf("only %d signed-in routes were checked", checked)
	}
}
