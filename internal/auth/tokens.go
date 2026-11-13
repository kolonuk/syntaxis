// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// NewToken returns a random URL-safe token and its storage hash. Only the
// hash is stored; the token is shown/mailed once.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the hex SHA-256 of a token (tokens are high-entropy, so a
// fast hash is appropriate).
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// EqualConstTime compares two strings in constant time.
func EqualConstTime(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes

// NewRecoveryCodes returns n human-friendly codes (xxxxx-xxxxx) and their hashes.
func NewRecoveryCodes(n int) (codes, hashes []string, err error) {
	for i := 0; i < n; i++ {
		raw := make([]byte, 10)
		if _, err = rand.Read(raw); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j, c := range raw {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
		}
		codes = append(codes, sb.String())
		hashes = append(hashes, HashRecoveryCode(sb.String()))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalises and hashes a recovery code.
func HashRecoveryCode(code string) string {
	code = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	return HashToken("recovery:" + code)
}
