// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("%q %v", h, err)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, bad := range []string{"", "x", "$argon2id$v=19$m=1,t=1,p=1$$", "$bcrypt$a$b$c$d",
		"$argon2id$v=19$m=999999999,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=65536,t=99,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=65536,t=2,p=2$c2FsdA$a2V5", "$argon2id$v=19$m=65536,t=2,p=0$c2FsdA$a2V5"} {
		if VerifyPassword("x", bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPolicy(t *testing.T) {
	bad := map[string]string{
		"short":             "short",
		"common":            "password123",
		"common-with-digit": "Password1234",
		"same":              "aaaaaaaaaaaaaa",
		"email":             "alice.smith-2024",
		"name":              "ilovewonderland1",
	}
	_ = bad
	cases := []struct {
		pw, email, name string
		ok              bool
	}{
		{"short", "a@example.com", "A", false},
		{"password123", "a@example.com", "A", false},
		{"Password12345", "a@example.com", "A", false}, // 'password' + digits
		{"aaaaaaaaaaaaaa", "a@example.com", "A", false},
		{"my-alice-secret-pw", "alice@example.com", "Bob", false},
		{"wonderland-rocks!", "x@example.com", "Wonderland", false},
		{"correct horse battery staple", "a@example.com", "Bob Jones", true},
		{"Zk3-hQ9v-Lp2m-Xw7r", "a@example.com", "Bob", true},
	}
	for _, c := range cases {
		err := CheckPasswordPolicy(c.pw, c.email, c.name)
		if c.ok && err != nil {
			t.Errorf("%q rejected: %v", c.pw, err)
		}
		if !c.ok && !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%q accepted", c.pw)
		}
	}
	if err := CheckPasswordPolicy(strings.Repeat("ab", 200), "a@b.c", "x"); err == nil {
		t.Error("absurdly long password accepted")
	}
}

func TestTokens(t *testing.T) {
	tok, h, err := NewToken()
	if err != nil || len(tok) < 40 || HashToken(tok) != h || tok == h {
		t.Fatal("token")
	}
	tok2, _, _ := NewToken()
	if tok == tok2 {
		t.Fatal("not random")
	}
	if !EqualConstTime("a", "a") || EqualConstTime("a", "b") {
		t.Fatal("eq")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes(10)
	if err != nil || len(codes) != 10 || len(hashes) != 10 {
		t.Fatal("gen")
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
		// forgiving input normalisation
		if HashRecoveryCode(strings.ToUpper(" "+c+" ")) != hashes[i] {
			t.Fatalf("normalisation failed for %q", c)
		}
	}
}
