// SPDX-License-Identifier: GPL-2.0-or-later

// Package auth implements accounts, sessions, 2FA and password policy for
// both admin accounts and chat users (kept strictly separate).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP-recommended minimum class). Tuned for a small server.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// MinPasswordLen is the minimum accepted password length.
const MinPasswordLen = 12

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks pw against an encoded hash in constant time.
func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	// refuse absurd parameters from a corrupted/hostile row (DoS guard)
	if m > 1<<20 || t > 10 || p == 0 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified when an account does not exist so timing does not
// reveal account existence.
var dummyHash, _ = HashPassword("dummy-password-for-timing")

// ErrWeakPassword wraps a human-readable policy failure.
var ErrWeakPassword = errors.New("weak password")

var commonPasswords = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`password password1 password123 passw0rd p@ssw0rd p@ssword
		123456 1234567 12345678 123456789 1234567890 12345678910 111111111111 000000000000
		qwerty qwerty123 qwertyuiop qwertyuiop123 1q2w3e4r 1q2w3e4r5t zaq12wsx asdfghjkl
		letmein letmein123 welcome welcome1 welcome123 admin admin123 administrator
		iloveyou monkey dragon football baseball sunshine princess trustno1 abc123 abcdefgh
		changeme changeme123 default secret secret123 login master superman batman`) {
		commonPasswords[p] = true
	}
}

// CheckPasswordPolicy validates length, common-password list and personal data.
func CheckPasswordPolicy(pw, email, name string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return fmt.Errorf("%w: must be at least %d characters", ErrWeakPassword, MinPasswordLen)
	}
	if len(pw) > 256 {
		return fmt.Errorf("%w: too long", ErrWeakPassword)
	}
	l := strings.ToLower(pw)
	if commonPasswords[l] {
		return fmt.Errorf("%w: too common", ErrWeakPassword)
	}
	stripped := strings.Trim(l, "0123456789!@#$%^&*()-_=+ ")
	if commonPasswords[stripped] {
		return fmt.Errorf("%w: too common", ErrWeakPassword)
	}
	if allSame(l) {
		return fmt.Errorf("%w: repeated characters", ErrWeakPassword)
	}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && len(local) >= 4 && strings.Contains(l, local) {
		return fmt.Errorf("%w: must not contain your email name", ErrWeakPassword)
	}
	if n := strings.ToLower(strings.TrimSpace(name)); len(n) >= 4 && strings.Contains(l, n) {
		return fmt.Errorf("%w: must not contain your name", ErrWeakPassword)
	}
	return nil
}

func allSame(s string) bool {
	r := []rune(s)
	for _, c := range r {
		if c != r[0] {
			return false
		}
	}
	return len(r) > 0
}
