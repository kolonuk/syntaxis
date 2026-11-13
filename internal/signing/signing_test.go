// SPDX-License-Identifier: GPL-2.0-or-later

package signing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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
	v, _ := vault.New(bytes.Repeat([]byte{3}, 32))
	return &Service{Store: st, Vault: v}, st
}

func TestKeyGenerationStableAndEncrypted(t *testing.T) {
	s, st := newSvc(t)
	p1, err := s.EnsureKey(ctx, 5)
	if err != nil || !strings.HasPrefix(p1, "ssh-ed25519 ") {
		t.Fatalf("%q %v", p1, err)
	}
	p2, _ := s.EnsureKey(ctx, 5)
	if p1 != p2 {
		t.Fatal("key regenerated")
	}
	p3, _ := s.EnsureKey(ctx, 6)
	if p3 == p1 {
		t.Fatal("users share a key")
	}
	row, _ := st.GetSecret(ctx, ScopeSigning, 5, "KEY")
	if bytes.Contains(row.Ciphertext, []byte("PRIVATE KEY")) || bytes.Contains(row.Ciphertext, []byte("openssh")) {
		t.Fatal("private key stored in plaintext")
	}
}

func TestSigningKeyNeverInSandboxEnvScope(t *testing.T) {
	s, st := newSvc(t)
	_, _ = s.EnsureKey(ctx, 5)
	rows, _ := st.ListSecrets(ctx, "user", 5)
	if len(rows) != 0 {
		t.Fatal("signing key visible in the user secret scope (which is injected into sandboxes)")
	}
}

func TestEmbedAndPayload(t *testing.T) {
	raw := "tree abc\nparent def\nauthor a <a@example.com> 1 +0000\ncommitter a <a@example.com> 1 +0000\n\nmsg\n"
	signed, err := Embed(raw, "-----BEGIN SSH SIGNATURE-----\nAAA\n-----END SSH SIGNATURE-----\n")
	if err != nil || !strings.Contains(signed, "gpgsig -----BEGIN SSH SIGNATURE-----\n AAA\n -----END SSH SIGNATURE-----\n\nmsg") {
		t.Fatalf("%q %v", signed, err)
	}
	if p, _ := Payload(signed); p != raw {
		t.Fatalf("payload differs:\n%q\n%q", p, raw)
	}
	again, _ := Embed(signed, "-----BEGIN SSH SIGNATURE-----\nBBB\n-----END SSH SIGNATURE-----\n")
	if strings.Contains(again, "AAA") || strings.Count(again, "gpgsig") != 1 {
		t.Fatal("old signature not replaced")
	}
	if _, err := Embed("garbage", "x"); !errors.Is(err, ErrNotACommit) {
		t.Fatal("garbage accepted")
	}
}

func git(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=u", "GIT_AUTHOR_EMAIL=u@example.com", "GIT_COMMITTER_NAME=u", "GIT_COMMITTER_EMAIL=u@example.com")
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	var out, eb bytes.Buffer
	c.Stdout, c.Stderr = &out, &eb
	if err := c.Run(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, eb.String())
	}
	return strings.TrimSpace(out.String())
}

// End to end: sign a real commit and have real git + ssh-keygen verify it.
func TestRealGitVerifiesSignature(t *testing.T) {
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	s, _ := newSvc(t)
	pub, _ := s.EnsureKey(ctx, 1)
	dir := t.TempDir()
	git(t, dir, "", "init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	git(t, dir, "", "add", ".")
	git(t, dir, "", "commit", "-q", "-m", "agent change")
	raw := git(t, dir, "", "cat-file", "commit", "HEAD") + "\n"
	payload, _ := Payload(raw)
	armored, err := s.Sign(ctx, 1, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := Embed(raw, armored)
	sha := git(t, dir, signed, "hash-object", "-t", "commit", "-w", "--stdin")
	git(t, dir, "", "update-ref", "HEAD", sha)
	allowed := filepath.Join(dir, "allowed")
	_ = os.WriteFile(allowed, []byte(AllowedSigners("u@example.com", pub)), 0o600)
	git(t, dir, "", "config", "gpg.format", "ssh")
	git(t, dir, "", "config", "gpg.ssh.allowedSignersFile", allowed)
	if out := git(t, dir, "", "log", "-1", "--format=%G?"); out != "G" {
		t.Fatalf("git did not accept the signature (status %q)", out)
	}
	// tampering invalidates it
	tampered := strings.Replace(signed, "agent change", "evil change", 1)
	sha2 := git(t, dir, tampered, "hash-object", "-t", "commit", "-w", "--stdin")
	git(t, dir, "", "update-ref", "HEAD", sha2)
	if out := git(t, dir, "", "log", "-1", "--format=%G?"); out == "G" {
		t.Fatal("tampered commit verified")
	}
}

func TestSignWithoutKey(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Sign(ctx, 9, []byte("x")); err == nil {
		t.Fatal("signed without a key")
	}
}
