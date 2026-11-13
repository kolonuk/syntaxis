// SPDX-License-Identifier: GPL-2.0-or-later

// Package signing implements SSH commit signing (gpg.format=ssh) with keys that
// never enter the sandbox or the model context. Signing happens on the server:
// the agent makes an unsigned commit, the server signs the commit object, and
// the signed object replaces it. The sandbox only ever sees the public key.
package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
	"golang.org/x/crypto/ssh"
)

// Modes.
const (
	ModeOff      = "off"
	ModeOptional = "optional"
	ModeRequired = "required"
)

// ScopeSigning is a dedicated vault scope: it is never listed for sandbox env
// injection (that only reads the "user" scope).
const ScopeSigning = "signing"

// Namespace is the SSHSIG namespace git uses.
const Namespace = "git"

// Service manages signing keys.
type Service struct {
	Store store.Store
	Vault *vault.Vault
}

// Instance owner id for the single bot key.
const InstanceOwner int64 = 0

func aad(owner int64) string { return fmt.Sprintf("signing:%d", owner) }

// EnsureKey returns the public key (authorized_keys format), generating a key
// pair on first use. owner 0 is the instance bot key.
func (s *Service) EnsureKey(ctx context.Context, owner int64) (string, error) {
	if row, err := s.Store.GetSecret(ctx, ScopeSigning, owner, "KEY"); err == nil {
		signer, err := s.signer(row.Ciphertext, owner)
		if err != nil {
			return "", err
		}
		return string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "syntaxis")
	if err != nil {
		return "", err
	}
	sealed, err := s.Vault.Seal(pem.EncodeToMemory(blk), aad(owner))
	if err != nil {
		return "", err
	}
	if err := s.Store.PutSecret(ctx, store.Secret{Scope: ScopeSigning, OwnerID: owner, Name: "KEY", Ciphertext: sealed, CreatedAt: time.Now()}); err != nil {
		return "", err
	}
	_, _ = s.Store.AppendAudit(ctx, "system", "signing.key.create", fmt.Sprintf("owner:%d", owner), "")
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(sshPub))), nil
}

func (s *Service) signer(sealed []byte, owner int64) (ssh.Signer, error) {
	pt, err := s.Vault.Open(sealed, aad(owner))
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(pt)
}

// Sign returns the armored SSHSIG for message, in the git namespace.
func (s *Service) Sign(ctx context.Context, owner int64, message []byte) (string, error) {
	row, err := s.Store.GetSecret(ctx, ScopeSigning, owner, "KEY")
	if err != nil {
		return "", fmt.Errorf("no signing key: %w", err)
	}
	signer, err := s.signer(row.Ciphertext, owner)
	if err != nil {
		return "", err
	}
	return SignSSH(signer, Namespace, message)
}

type sshsigBlob struct {
	Magic     [6]byte
	Version   uint32
	PublicKey string
	Namespace string
	Reserved  string
	HashAlg   string
	Signature string
}

type sshsigSigned struct {
	Magic     [6]byte
	Namespace string
	Reserved  string
	HashAlg   string
	Hash      string
}

// SignSSH produces an armored SSHSIG (PROTOCOL.sshsig) over message.
func SignSSH(signer ssh.Signer, namespace string, message []byte) (string, error) {
	h := sha512.Sum512(message)
	toSign := ssh.Marshal(sshsigSigned{Magic: [6]byte{'S', 'S', 'H', 'S', 'I', 'G'}, Namespace: namespace, HashAlg: "sha512", Hash: string(h[:])})
	sig, err := signer.Sign(rand.Reader, toSign)
	if err != nil {
		return "", err
	}
	blob := ssh.Marshal(sshsigBlob{
		Magic: [6]byte{'S', 'S', 'H', 'S', 'I', 'G'}, Version: 1,
		PublicKey: string(signer.PublicKey().Marshal()), Namespace: namespace, HashAlg: "sha512",
		Signature: string(ssh.Marshal(sig)),
	})
	b64 := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(b64) > 70 {
		out.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	out.WriteString(b64 + "\n-----END SSH SIGNATURE-----\n")
	return out.String(), nil
}

// ErrNotACommit is returned when the object text is not a commit.
var ErrNotACommit = errors.New("signing: not a commit object")

// Embed inserts the armored signature into a raw commit object as a gpgsig
// header (the format `git cat-file commit` prints and `git hash-object -t
// commit` accepts). Any existing gpgsig header is replaced.
func Embed(rawCommit, armored string) (string, error) {
	hdr, body, ok := strings.Cut(rawCommit, "\n\n")
	if !ok || !strings.HasPrefix(hdr, "tree ") {
		return "", ErrNotACommit
	}
	// drop an existing gpgsig header block
	var lines []string
	skip := false
	for _, l := range strings.Split(hdr, "\n") {
		if strings.HasPrefix(l, "gpgsig ") {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(l, " ") {
			continue
		}
		skip = false
		lines = append(lines, l)
	}
	sig := strings.Split(strings.TrimRight(armored, "\n"), "\n")
	for i, l := range sig {
		if i == 0 {
			lines = append(lines, "gpgsig "+l)
		} else {
			lines = append(lines, " "+l)
		}
	}
	return strings.Join(lines, "\n") + "\n\n" + body, nil
}

// Payload returns the commit text git signs: the raw object without any gpgsig header.
func Payload(rawCommit string) (string, error) {
	hdr, body, ok := strings.Cut(rawCommit, "\n\n")
	if !ok || !strings.HasPrefix(hdr, "tree ") {
		return "", ErrNotACommit
	}
	var lines []string
	skip := false
	for _, l := range strings.Split(hdr, "\n") {
		if strings.HasPrefix(l, "gpgsig ") {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(l, " ") {
			continue
		}
		skip = false
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n") + "\n\n" + body, nil
}

// AllowedSigners renders a line for git's gpg.ssh.allowedSignersFile.
func AllowedSigners(email, pubKey string) string {
	return fmt.Sprintf("%s namespaces=\"git\" %s\n", email, pubKey)
}
