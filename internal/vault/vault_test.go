// SPDX-License-Identifier: GPL-2.0-or-later

package vault

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func newV(t *testing.T) *Vault {
	t.Helper()
	k, _ := GenerateKey()
	b, _ := base64.StdEncoding.DecodeString(k)
	v, err := New(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRoundTrip(t *testing.T) {
	v := newV(t)
	ct, err := v.Seal([]byte("hunter2"), "user1/gh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hunter2")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := v.Open(ct, "user1/gh")
	if err != nil || string(pt) != "hunter2" {
		t.Fatalf("got %q err %v", pt, err)
	}
}

func TestAADBinding(t *testing.T) {
	v := newV(t)
	ct, _ := v.Seal([]byte("x"), "user1/gh")
	if _, err := v.Open(ct, "user2/gh"); err == nil {
		t.Fatal("ciphertext opened under another owner's aad")
	}
}

func TestTamperAndShort(t *testing.T) {
	v := newV(t)
	ct, _ := v.Seal([]byte("x"), "a")
	ct[len(ct)-1] ^= 1
	if _, err := v.Open(ct, "a"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := v.Open([]byte{1, 2}, "a"); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	ct2, _ := v.Seal([]byte("x"), "a")
	ct2[0] = 9
	if _, err := v.Open(ct2, "a"); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestNonceUnique(t *testing.T) {
	v := newV(t)
	a, _ := v.Seal([]byte("x"), "a")
	b, _ := v.Seal([]byte("x"), "a")
	if bytes.Equal(a, b) {
		t.Fatal("identical ciphertexts: nonce reuse")
	}
}

func TestBadKeyLen(t *testing.T) {
	if _, err := New(make([]byte, 5)); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestRotate(t *testing.T) {
	a, b := newV(t), newV(t)
	ct, _ := a.Seal([]byte("secret"), "k")
	ct2, err := Rotate(a, b, ct, "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(ct2, "k"); err == nil {
		t.Fatal("old key still opens rotated data")
	}
	pt, err := b.Open(ct2, "k")
	if err != nil || string(pt) != "secret" {
		t.Fatalf("rotated open: %q %v", pt, err)
	}
	if _, err := Rotate(b, a, []byte("junk"), "k"); err == nil {
		t.Fatal("rotate of junk succeeded")
	}
}
