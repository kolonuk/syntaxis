// SPDX-License-Identifier: GPL-2.0-or-later

// Package vault encrypts secrets at rest with AES-256-GCM.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const version byte = 1

// Vault encrypts and decrypts small secrets with a master key.
type Vault struct {
	aead cipher.AEAD
}

// New creates a Vault from a 32-byte key.
func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("vault: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

// GenerateKey returns a new random base64-encoded 32-byte key.
func GenerateKey() (string, error) {
	k := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k), nil
}

// Seal encrypts plaintext. The associated data (e.g. owner + name) binds the
// ciphertext to its row so it cannot be swapped between users.
func (v *Vault) Seal(plaintext []byte, aad string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{version}, nonce...)
	return v.aead.Seal(out, nonce, plaintext, []byte(aad)), nil
}

// ErrDecrypt is returned for any failure to open a ciphertext.
var ErrDecrypt = errors.New("vault: cannot decrypt")

// Open decrypts ciphertext produced by Seal with the same aad.
func (v *Vault) Open(ct []byte, aad string) ([]byte, error) {
	ns := v.aead.NonceSize()
	if len(ct) < 1+ns+v.aead.Overhead() || ct[0] != version {
		return nil, ErrDecrypt
	}
	pt, err := v.aead.Open(nil, ct[1:1+ns], ct[1+ns:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Rotate re-encrypts ct from old to next under the same aad.
func Rotate(old, next *Vault, ct []byte, aad string) ([]byte, error) {
	pt, err := old.Open(ct, aad)
	if err != nil {
		return nil, err
	}
	return next.Seal(pt, aad)
}
