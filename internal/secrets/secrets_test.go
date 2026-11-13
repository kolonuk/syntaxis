// SPDX-License-Identifier: GPL-2.0-or-later

package secrets

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
)

var ctx = context.Background()

func newSvc(t *testing.T) (*Service, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, _ := vault.New(bytes.Repeat([]byte{7}, 32))
	return &Service{Store: st, Vault: v}, st
}

func TestEncryptedAtRest(t *testing.T) {
	s, st := newSvc(t)
	if err := s.Put(ctx, ScopeUser, 1, "GITHUB_TOKEN", "ghp_supersecretvalue"); err != nil {
		t.Fatal(err)
	}
	row, _ := st.GetSecret(ctx, ScopeUser, 1, "GITHUB_TOKEN")
	if bytes.Contains(row.Ciphertext, []byte("supersecret")) {
		t.Fatal("plaintext in the database")
	}
	v, err := s.Get(ctx, ScopeUser, 1, "GITHUB_TOKEN")
	if err != nil || v != "ghp_supersecretvalue" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestCiphertextCannotBeMovedBetweenUsers(t *testing.T) {
	s, st := newSvc(t)
	_ = s.Put(ctx, ScopeUser, 1, "API_KEY", "alice-key-value")
	row, _ := st.GetSecret(ctx, ScopeUser, 1, "API_KEY")
	// a DB-level attacker copies Alice's row to Bob
	_ = st.PutSecret(ctx, store.Secret{Scope: ScopeUser, OwnerID: 2, Name: "API_KEY", Ciphertext: row.Ciphertext})
	if _, err := s.Get(ctx, ScopeUser, 2, "API_KEY"); err == nil {
		t.Fatal("Alice's ciphertext decrypted under Bob's identity")
	}
}

func TestUserIsolation(t *testing.T) {
	s, _ := newSvc(t)
	_ = s.Put(ctx, ScopeUser, 1, "TOKEN", "alice-value-1")
	_ = s.Put(ctx, ScopeUser, 2, "TOKEN", "bob-value-22")
	a, _ := s.Get(ctx, ScopeUser, 1, "TOKEN")
	b, _ := s.Get(ctx, ScopeUser, 2, "TOKEN")
	if a != "alice-value-1" || b != "bob-value-22" {
		t.Fatal("crossed")
	}
	if _, err := s.Get(ctx, ScopeUser, 3, "TOKEN"); err == nil {
		t.Fatal("user 3 reads a secret")
	}
	names, _ := s.Names(ctx, ScopeUser, 1)
	if len(names) != 1 || names[0] != "TOKEN" {
		t.Fatal(names)
	}
}

func TestNameValidation(t *testing.T) {
	s, _ := newSvc(t)
	for _, bad := range []string{"lower", "1START", "HAS SPACE", "A-B", "", "PATH", "LD_PRELOAD", "DOCKER_HOST", strings.Repeat("A", 65)} {
		if err := s.Put(ctx, ScopeUser, 1, bad, "v"); !errors.Is(err, ErrBadName) {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := s.Put(ctx, "weird", 1, "OK", "v"); err == nil {
		t.Fatal("bad scope")
	}
}

func TestSandboxEnvExcludesInstanceSecrets(t *testing.T) {
	s, _ := newSvc(t)
	_ = s.Put(ctx, ScopeInstance, 99, "PROVIDER_KEY", "instance-key-value")
	_ = s.Put(ctx, ScopeUser, 1, "CLOUDFLARE_API_TOKEN", "cf-token-value-1")
	_ = s.Put(ctx, ScopeUser, 2, "OTHER_USERS_TOKEN", "bobs-token-value")
	env, err := s.SandboxEnv(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env["CLOUDFLARE_API_TOKEN"] != "cf-token-value-1" {
		t.Fatalf("%v", env)
	}
	if _, ok := env["PROVIDER_KEY"]; ok {
		t.Fatal("instance secret leaked to a sandbox")
	}
}

func TestRedactorKnowsUserSecrets(t *testing.T) {
	s, _ := newSvc(t)
	_ = s.Put(ctx, ScopeUser, 1, "DEPLOY_PASSWORD", "hunter2-hunter2")
	r, _ := s.Redactor(ctx, 1)
	if out := r.String("the password is hunter2-hunter2 ok"); strings.Contains(out, "hunter2") {
		t.Fatalf("%q", out)
	}
	r2, _ := s.Redactor(ctx, 2)
	if out := r2.String("hunter2-hunter2"); !strings.Contains(out, "hunter2") {
		t.Fatal("another user's redactor must not know this secret (it is not theirs)")
	}
}

func TestAuditNeverContainsValues(t *testing.T) {
	s, st := newSvc(t)
	_ = s.Put(ctx, ScopeUser, 1, "TOKEN", "the-secret-value-xyz")
	_, _ = s.Get(ctx, ScopeUser, 1, "TOKEN")
	_ = s.Delete(ctx, ScopeUser, 1, "TOKEN")
	es, _ := st.ListAudit(ctx, 0)
	if len(es) != 3 {
		t.Fatalf("secret access must be audited: %+v", es)
	}
	for _, e := range es {
		if strings.Contains(e.Detail+e.Target+e.Action, "the-secret-value") {
			t.Fatal("value in audit log")
		}
	}
	if err := s.Delete(ctx, ScopeUser, 1, "TOKEN"); err == nil {
		t.Fatal("double delete")
	}
}

func TestRotateAll(t *testing.T) {
	s, st := newSvc(t)
	_ = s.Put(ctx, ScopeInstance, 0, "PROV", "instance-value-1")
	_ = s.Put(ctx, ScopeUser, 1, "TOK", "user-one-value-1")
	_ = s.Put(ctx, ScopeUser, 2, "TOK", "user-two-value-2")
	next, _ := vault.New(bytes.Repeat([]byte{9}, 32))
	n, err := RotateAll(ctx, st, s.Vault, next, []int64{1, 2})
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := s.Get(ctx, ScopeUser, 1, "TOK"); err == nil {
		t.Fatal("old key still decrypts")
	}
	s2 := &Service{Store: st, Vault: next}
	if v, _ := s2.Get(ctx, ScopeUser, 2, "TOK"); v != "user-two-value-2" {
		t.Fatal("rotation corrupted data")
	}
	if v, _ := s2.Get(ctx, ScopeInstance, 0, "PROV"); v != "instance-value-1" {
		t.Fatal("instance secret")
	}
}
