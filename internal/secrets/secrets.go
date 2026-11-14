// SPDX-License-Identifier: GPL-2.0-or-later

// Package secrets is the encrypted vault service. Secrets live in the database
// as AES-256-GCM ciphertext bound to (scope, owner, name). Instance secrets
// (provider keys) are never injected into sandboxes; user secrets are injected
// only while that user's sandbox runs.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/kolonuk/syntaxis/internal/redact"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
)

// Scopes.
const (
	ScopeInstance = "instance"
	ScopeUser     = "user"
)

var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Names that must never be injected into a sandbox environment.
var forbiddenEnv = map[string]bool{"PATH": true, "HOME": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "SHELL": true, "USER": true, "PWD": true, "DOCKER_HOST": true, "WORKSPACE": true}

// ErrBadName is returned for invalid secret names.
var ErrBadName = errors.New("secret names must be UPPER_SNAKE_CASE (A-Z, 0-9, _), max 64 characters")

// Service wraps the store and vault.
type Service struct {
	Store store.Store
	Vault *vault.Vault
	Now   func() time.Time
}

func aad(scope string, owner int64, name string) string {
	return fmt.Sprintf("%s:%d:%s", scope, owner, name)
}

// Put stores or replaces a secret.
func (s *Service) Put(ctx context.Context, scope string, owner int64, name, value string) error {
	if !nameRe.MatchString(name) || forbiddenEnv[name] {
		return ErrBadName
	}
	if scope != ScopeInstance && scope != ScopeUser {
		return errors.New("secrets: bad scope")
	}
	if scope == ScopeInstance {
		owner = 0
	}
	ct, err := s.Vault.Seal([]byte(value), aad(scope, owner, name))
	if err != nil {
		return err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if err := s.Store.PutSecret(ctx, store.Secret{Scope: scope, OwnerID: owner, Name: name, Ciphertext: ct, CreatedAt: now}); err != nil {
		return err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", scope, owner), "secret.put", name, "")
	return nil
}

// Get decrypts one secret. Access is audited.
func (s *Service) Get(ctx context.Context, scope string, owner int64, name string) (string, error) {
	if scope == ScopeInstance {
		owner = 0
	}
	row, err := s.Store.GetSecret(ctx, scope, owner, name)
	if err != nil {
		return "", err
	}
	pt, err := s.Vault.Open(row.Ciphertext, aad(scope, owner, name))
	if err != nil {
		return "", err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", scope, owner), "secret.read", name, "")
	return string(pt), nil
}

// Names lists secret names (never values).
func (s *Service) Names(ctx context.Context, scope string, owner int64) ([]string, error) {
	if scope == ScopeInstance {
		owner = 0
	}
	rows, err := s.Store.ListSecrets(ctx, scope, owner)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out, nil
}

// Delete removes a secret.
func (s *Service) Delete(ctx context.Context, scope string, owner int64, name string) error {
	if scope == ScopeInstance {
		owner = 0
	}
	if err := s.Store.DeleteSecret(ctx, scope, owner, name); err != nil {
		return err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", scope, owner), "secret.delete", name, "")
	return nil
}

// SandboxEnv returns the user's secrets as environment variables for the
// sandbox. Only user-scope secrets are included; instance secrets never are.
// Callers pass the result straight to sandbox creation and discard it; the
// variables vanish when the container is removed or stopped.
func (s *Service) SandboxEnv(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := s.Store.ListSecrets(ctx, ScopeUser, userID)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, r := range rows {
		pt, err := s.Vault.Open(r.Ciphertext, aad(ScopeUser, userID, r.Name))
		if err != nil {
			continue
		}
		env[r.Name] = string(pt)
	}
	return env, nil
}

// Snapshot returns the user's current secrets as environment variables together with
// a redactor that knows every value. It is read fresh each time, so a secret added or
// removed in Settings applies to the very next command.
func (s *Service) Snapshot(ctx context.Context, userID int64) (map[string]string, *redact.Redactor, error) {
	env, err := s.SandboxEnv(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	vals := make([]string, 0, len(env))
	for _, v := range env {
		vals = append(vals, v)
	}
	return env, redact.New(vals...), nil
}

// Redactor builds a redactor that knows all of a user's secret values, so they
// are scrubbed from model input, logs and the UI stream.
func (s *Service) Redactor(ctx context.Context, userID int64) (*redact.Redactor, error) {
	env, err := s.SandboxEnv(ctx, userID)
	if err != nil {
		return nil, err
	}
	var vals []string
	for _, v := range env {
		vals = append(vals, v)
	}
	return redact.New(vals...), nil
}

// RotateAll re-encrypts every secret from old to next (master key rotation).
// It returns the number of rows rotated. Run offline; verify before discarding the old key.
func RotateAll(ctx context.Context, st store.Store, old, next *vault.Vault, ownerIDs []int64) (int, error) {
	n := 0
	scopes := []struct {
		scope string
		owner int64
	}{{ScopeInstance, 0}}
	for _, id := range ownerIDs {
		scopes = append(scopes, struct {
			scope string
			owner int64
		}{ScopeUser, id})
	}
	for _, sc := range scopes {
		rows, err := st.ListSecrets(ctx, sc.scope, sc.owner)
		if err != nil {
			return n, err
		}
		for _, r := range rows {
			ct, err := vault.Rotate(old, next, r.Ciphertext, aad(sc.scope, sc.owner, r.Name))
			if err != nil {
				return n, fmt.Errorf("rotate %s/%d/%s: %w", sc.scope, sc.owner, r.Name, err)
			}
			r.Ciphertext = ct
			if err := st.PutSecret(ctx, r); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
