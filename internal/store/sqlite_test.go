// SPDX-License-Identifier: GPL-2.0-or-later

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/audit"
)

func open(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var ctx = context.Background()

func TestAdminAndUserTablesSeparate(t *testing.T) {
	s := open(t)
	now := time.Now()
	aid, err := s.CreateAccount(ctx, Account{Kind: KindAdmin, Email: "A@Example.com", Name: "a", PasswordHash: "h", CreatedAt: now, PasswordChangedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAccountByEmail(ctx, KindUser, "a@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin email visible in users table: %v", err)
	}
	got, err := s.GetAccountByEmail(ctx, KindAdmin, "a@EXAMPLE.com")
	if err != nil || got.ID != aid || got.Email != "a@example.com" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	// same email may exist as a user: two accounts for a person needing both
	if _, err := s.CreateAccount(ctx, Account{Kind: KindUser, Email: "a@example.com", Name: "a", CreatedAt: now}); err != nil {
		t.Fatalf("same email as user must be allowed: %v", err)
	}
	if _, err := s.CreateAccount(ctx, Account{Kind: KindAdmin, Email: "a@example.com", Name: "dup", CreatedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if _, err := s.CreateAccount(ctx, Account{Kind: "x"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestUpdateAccountAndCount(t *testing.T) {
	s := open(t)
	now := time.Now()
	id, _ := s.CreateAccount(ctx, Account{Kind: KindUser, Email: "u@example.com", Name: "u", CreatedAt: now})
	a, _ := s.GetAccount(ctx, KindUser, id)
	a.TOTPEnabled, a.PasswordHash = true, "newhash"
	a.Remind2FAAfter = now.Add(time.Hour)
	if err := s.UpdateAccount(ctx, *a); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetAccount(ctx, KindUser, id)
	if !b.TOTPEnabled || b.PasswordHash != "newhash" || b.Remind2FAAfter.IsZero() {
		t.Fatalf("update lost: %+v", b)
	}
	if err := s.UpdateAccount(ctx, Account{Kind: KindUser, ID: 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	n, _ := s.CountAccounts(ctx, KindUser)
	m, _ := s.CountAccounts(ctx, KindAdmin)
	if n != 1 || m != 0 {
		t.Fatalf("counts %d %d", n, m)
	}
	l, _ := s.ListAccounts(ctx, KindUser)
	if len(l) != 1 {
		t.Fatal("list")
	}
}

func TestUserPolicy(t *testing.T) {
	s := open(t)
	id, _ := s.CreateAccount(ctx, Account{Kind: KindUser, Email: "u@example.com", Name: "u", CreatedAt: time.Now()})
	u, _ := s.GetUser(ctx, id)
	u.QuotaDailyTokens, u.MaxSessions, u.EgressMode = 1000, 2, "none"
	u.AllowedModels = []string{"p/m1", "p/m2"}
	u.CloudToolsDisabled = true
	if err := s.UpdateUserPolicy(ctx, *u); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetUser(ctx, id)
	if g.QuotaDailyTokens != 1000 || g.MaxSessions != 2 || g.EgressMode != "none" || len(g.AllowedModels) != 2 || !g.CloudToolsDisabled {
		t.Fatalf("%+v", g)
	}
	us, _ := s.ListUsers(ctx)
	if len(us) != 1 {
		t.Fatal("list users")
	}
	if _, err := s.GetUser(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatal("want not found")
	}
}

func TestRecoveryCodes(t *testing.T) {
	s := open(t)
	if err := s.ReplaceRecoveryCodes(ctx, KindUser, 1, []string{"h1", "h2", "h3"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.UseRecoveryCode(ctx, KindUser, 1, "h2"); !ok {
		t.Fatal("valid code rejected")
	}
	if ok, _ := s.UseRecoveryCode(ctx, KindUser, 1, "h2"); ok {
		t.Fatal("code reusable")
	}
	if ok, _ := s.UseRecoveryCode(ctx, KindUser, 2, "h1"); ok {
		t.Fatal("code valid for another owner")
	}
	if ok, _ := s.UseRecoveryCode(ctx, KindAdmin, 1, "h1"); ok {
		t.Fatal("code valid for another kind")
	}
	if n, _ := s.CountRecoveryCodes(ctx, KindUser, 1); n != 2 {
		t.Fatalf("count %d", n)
	}
	_ = s.ReplaceRecoveryCodes(ctx, KindUser, 1, []string{"x"})
	if n, _ := s.CountRecoveryCodes(ctx, KindUser, 1); n != 1 {
		t.Fatalf("replace count %d", n)
	}
}

func TestTokensSingleUseAndExpiry(t *testing.T) {
	s := open(t)
	now := time.Now()
	_ = s.PutToken(ctx, Token{Hash: "a", Kind: "invite", UserID: 1, ExpiresAt: now.Add(time.Hour)})
	_ = s.PutToken(ctx, Token{Hash: "b", Kind: "invite", UserID: 1, ExpiresAt: now.Add(-time.Hour)})
	if _, err := s.ConsumeToken(ctx, "a", "reset", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong kind consumed")
	}
	tk, err := s.ConsumeToken(ctx, "a", "invite", now)
	if err != nil || tk.UserID != 1 {
		t.Fatalf("consume: %v", err)
	}
	if _, err := s.ConsumeToken(ctx, "a", "invite", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("token reusable")
	}
	if _, err := s.ConsumeToken(ctx, "b", "invite", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired token consumed")
	}
	if err := s.PutToken(ctx, Token{Hash: "a", Kind: "invite", UserID: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate hash accepted")
	}
}

func TestAuthSessions(t *testing.T) {
	s := open(t)
	now := time.Now()
	_ = s.PutAuthSession(ctx, AuthSession{IDHash: "s1", Kind: KindUser, OwnerID: 5, CSRF: "c", CreatedAt: now, ExpiresAt: now.Add(time.Hour), PendingMFA: true})
	_ = s.PutAuthSession(ctx, AuthSession{IDHash: "s2", Kind: KindUser, OwnerID: 5, CSRF: "c", CreatedAt: now, ExpiresAt: now.Add(-time.Hour)})
	_ = s.PutAuthSession(ctx, AuthSession{IDHash: "s3", Kind: KindAdmin, OwnerID: 5, CSRF: "c", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	g, err := s.GetAuthSession(ctx, "s1")
	if err != nil || !g.PendingMFA {
		t.Fatal("get")
	}
	g.PendingMFA, g.CSRF = false, "new"
	if err := s.UpdateAuthSession(ctx, *g); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GetAuthSession(ctx, "s1")
	if g.PendingMFA || g.CSRF != "new" {
		t.Fatal("update lost")
	}
	if n, _ := s.PurgeExpiredAuthSessions(ctx, now); n != 1 {
		t.Fatalf("purged %d", n)
	}
	// logout everywhere only affects that kind+owner
	_ = s.DeleteAuthSessionsFor(ctx, KindUser, 5)
	if _, err := s.GetAuthSession(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("user session survived revoke")
	}
	if _, err := s.GetAuthSession(ctx, "s3"); err != nil {
		t.Fatal("admin session wrongly revoked")
	}
	_ = s.DeleteAuthSession(ctx, "s3")
	if _, err := s.GetAuthSession(ctx, "s3"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete")
	}
}

func TestAttempts(t *testing.T) {
	s := open(t)
	now := time.Now()
	_ = s.PutAttempt(ctx, Attempt{Key: "ip:1", Failures: 3, WindowStart: now})
	_ = s.PutAttempt(ctx, Attempt{Key: "ip:1", Failures: 4, WindowStart: now, LockedUntil: now.Add(time.Minute)})
	a, err := s.GetAttempt(ctx, "ip:1")
	if err != nil || a.Failures != 4 || a.LockedUntil.IsZero() {
		t.Fatalf("%+v %v", a, err)
	}
	_ = s.ClearAttempt(ctx, "ip:1")
	if _, err := s.GetAttempt(ctx, "ip:1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("clear")
	}
}

func TestProvidersAndModels(t *testing.T) {
	s := open(t)
	pid, err := s.UpsertProvider(ctx, Provider{Name: "p1", Kind: "ollama", BaseURL: "http://llm.internal:11434", Concurrency: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertProvider(ctx, Provider{Name: "p1", Kind: "x", BaseURL: "y"}); !errors.Is(err, ErrConflict) {
		t.Fatal("dup name")
	}
	mid, _ := s.UpsertModel(ctx, Model{ProviderID: pid, Name: "m", ToolsSupported: -1, SeenAt: time.Now()})
	_ = s.SetModelEnabled(ctx, mid, true)
	// rediscovery must not reset the admin's enabled flag
	mid2, _ := s.UpsertModel(ctx, Model{ProviderID: pid, Name: "m", Digest: "d2", ToolsSupported: 1, SeenAt: time.Now()})
	if mid2 != mid {
		t.Fatal("id changed")
	}
	ms, _ := s.ListModels(ctx, pid)
	if len(ms) != 1 || !ms[0].Enabled || ms[0].Digest != "d2" || ms[0].ToolsSupported != 1 {
		t.Fatalf("%+v", ms)
	}
	p, _ := s.GetProvider(ctx, pid)
	p.Concurrency = 4
	if _, err := s.UpsertProvider(ctx, *p); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.ListProviders(ctx)
	if len(ps) != 1 || ps[0].Concurrency != 4 {
		t.Fatal("update provider")
	}
	if err := s.DeleteProvider(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ListModels(ctx, 0); len(ms) != 0 {
		t.Fatal("models not cascaded")
	}
	if err := s.DeleteProvider(ctx, pid); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete")
	}
}

func TestSessionsMessagesRewind(t *testing.T) {
	s := open(t)
	uid, _ := s.CreateAccount(ctx, Account{Kind: KindUser, Email: "u@example.com", Name: "u", CreatedAt: time.Now()})
	now := time.Now()
	if err := s.CreateChatSession(ctx, ChatSession{ID: "s1", UserID: uid, Title: "t", Model: "m", Autonomy: "workspace", State: "idle", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range []string{"user", "assistant", "user", "assistant"} {
		id, err := s.AppendMessage(ctx, Message{SessionID: "s1", Role: r, Content: r, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	n, _ := s.AbandonMessagesAfter(ctx, "s1", ids[1])
	if n != 2 {
		t.Fatalf("abandoned %d", n)
	}
	live, _ := s.ListMessages(ctx, "s1", false)
	all, _ := s.ListMessages(ctx, "s1", true)
	if len(live) != 2 || len(all) != 4 {
		t.Fatalf("live %d all %d (transcript must never be discarded)", len(live), len(all))
	}
	c, _ := s.GetChatSession(ctx, "s1")
	c.Title, c.TokensIn = "renamed", 10
	if err := s.UpdateChatSession(ctx, *c); err != nil {
		t.Fatal(err)
	}
	l, _ := s.ListChatSessions(ctx, uid)
	if len(l) != 1 || l[0].Title != "renamed" {
		t.Fatal("update")
	}
	if other, _ := s.ListChatSessions(ctx, uid+1); len(other) != 0 {
		t.Fatal("another user's list leaked")
	}
	if err := s.DeleteChatSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChatSession(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted visible")
	}
	if a, _ := s.ListAllChatSessions(ctx); len(a) != 0 {
		t.Fatal("deleted in admin list")
	}
}

func TestUsage(t *testing.T) {
	s := open(t)
	_ = s.AddUsage(ctx, 1, "2026-11-01", 10, 5, 60)
	_ = s.AddUsage(ctx, 1, "2026-11-01", 1, 1, 0)
	_ = s.AddUsage(ctx, 1, "2026-11-02", 100, 0, 0)
	_ = s.AddUsage(ctx, 2, "2026-11-01", 999, 0, 0)
	d, _ := s.GetUsage(ctx, 1, "2026-11-01")
	if d.TokensIn != 11 || d.TokensOut != 6 || d.SandboxSeconds != 60 {
		t.Fatalf("%+v", d)
	}
	m, _ := s.SumUsage(ctx, 1, "2026-11")
	if m.TokensIn != 111 {
		t.Fatalf("month %+v", m)
	}
	z, _ := s.GetUsage(ctx, 1, "2030-01-01")
	if z.TokensIn != 0 {
		t.Fatal("absent day not zero")
	}
	_ = s.ResetUsage(ctx, 1, "2026-11-01")
	d, _ = s.GetUsage(ctx, 1, "2026-11-01")
	if d.TokensIn != 0 {
		t.Fatal("reset")
	}
}

func TestSecretsScopedByOwner(t *testing.T) {
	s := open(t)
	_ = s.PutSecret(ctx, Secret{Scope: "user", OwnerID: 1, Name: "gh", Ciphertext: []byte("c1"), CreatedAt: time.Now()})
	_ = s.PutSecret(ctx, Secret{Scope: "user", OwnerID: 2, Name: "gh", Ciphertext: []byte("c2"), CreatedAt: time.Now()})
	a, _ := s.GetSecret(ctx, "user", 1, "gh")
	b, _ := s.GetSecret(ctx, "user", 2, "gh")
	if string(a.Ciphertext) != "c1" || string(b.Ciphertext) != "c2" {
		t.Fatal("secrets crossed between users")
	}
	if _, err := s.GetSecret(ctx, "user", 3, "gh"); !errors.Is(err, ErrNotFound) {
		t.Fatal("user 3 sees a secret")
	}
	if _, err := s.GetSecret(ctx, "instance", 1, "gh"); !errors.Is(err, ErrNotFound) {
		t.Fatal("scope confusion")
	}
	_ = s.PutSecret(ctx, Secret{Scope: "user", OwnerID: 1, Name: "gh", Ciphertext: []byte("c1b")})
	a, _ = s.GetSecret(ctx, "user", 1, "gh")
	if string(a.Ciphertext) != "c1b" {
		t.Fatal("upsert")
	}
	l, _ := s.ListSecrets(ctx, "user", 1)
	if len(l) != 1 {
		t.Fatal("list")
	}
	if err := s.DeleteSecret(ctx, "user", 1, "gh"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, "user", 1, "gh"); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete")
	}
}

func TestAuditChainPersisted(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		if _, err := s.AppendAudit(ctx, "admin:1", "user.create", "user:2", "d"); err != nil {
			t.Fatal(err)
		}
	}
	es, err := s.ListAudit(ctx, 0)
	if err != nil || len(es) != 5 {
		t.Fatalf("%d %v", len(es), err)
	}
	if i := audit.Verify(es); i != -1 {
		t.Fatalf("persisted chain broken at %d", i)
	}
	if lim, _ := s.ListAudit(ctx, 2); len(lim) != 2 {
		t.Fatal("limit")
	}
	// tamper directly in the DB: verification must detect it
	if _, err := s.db.Exec(`UPDATE audit_log SET detail='evil' WHERE seq=3`); err != nil {
		t.Fatal(err)
	}
	es, _ = s.ListAudit(ctx, 0)
	if i := audit.Verify(es); i != 2 {
		t.Fatalf("tamper detected at %d, want 2", i)
	}
}

func TestAuditConcurrentLinear(t *testing.T) {
	s := open(t)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				_, _ = s.AppendAudit(ctx, "a", "x", "t", "d")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	es, _ := s.ListAudit(ctx, 0)
	if len(es) != 80 || audit.Verify(es) != -1 {
		t.Fatalf("n=%d verify=%d", len(es), audit.Verify(es))
	}
}

func TestEgressFilteredByUser(t *testing.T) {
	s := open(t)
	now := time.Now()
	_ = s.AddEgress(ctx, EgressEntry{Time: now, UserID: 1, SessionID: "a", DestHost: "x.example.com", Port: 443, Protocol: "tcp", Allowed: true})
	_ = s.AddEgress(ctx, EgressEntry{Time: now, UserID: 2, SessionID: "b", DestHost: "y.example.com", Port: 25, Protocol: "tcp", Allowed: false})
	if all, _ := s.ListEgress(ctx, 0, 10); len(all) != 2 {
		t.Fatal("admin should see all")
	}
	mine, _ := s.ListEgress(ctx, 1, 10)
	if len(mine) != 1 || mine[0].UserID != 1 {
		t.Fatalf("user sees %+v", mine)
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	if v, _ := s.GetSetting(ctx, "k", "def"); v != "def" {
		t.Fatal("default")
	}
	_ = s.SetSetting(ctx, "k", "v1")
	_ = s.SetSetting(ctx, "k", "v2")
	if v, _ := s.GetSetting(ctx, "k", "def"); v != "v2" {
		t.Fatal("upsert")
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.db")
	s, err := OpenSQLite(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s2, err := OpenSQLite(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s2.Close()
}
