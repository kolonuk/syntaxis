// SPDX-License-Identifier: GPL-2.0-or-later

package scanner

import (
	"strings"
	"testing"
)

// Fixtures are built at runtime so this source file itself never contains a
// string that looks like a real credential.
func fake(prefix string, n int) string { return prefix + strings.Repeat("aB3dE5gH7j", n)[:n] }

func TestRules(t *testing.T) {
	cases := map[string]string{
		"aws-access-key-id":       "key = " + "AKIA" + "ABCDEFGHIJKLMNOP",
		"github-token":            "token " + fake("ghp_", 40),
		"gitlab-pat":              "x " + fake("glpat-", 26),
		"slack-token":             "x " + fake("xoxb-", 30),
		"stripe-secret-key":       "x " + fake("sk_live_", 30),
		"private-key-block":       "-----BEGIN OPENSSH PRIVATE KEY-----",
		"google-api-key":          "k=" + "AIza" + strings.Repeat("a1B2c3D", 5),
		"github-fine-grained-pat": "x " + fake("github_pat_", 70),
	}
	for rule, in := range cases {
		fs := Scan(in)
		found := false
		for _, f := range fs {
			if f.Rule == rule {
				found = true
			}
		}
		if !found {
			t.Errorf("rule %s not triggered for %q (got %+v)", rule, in, fs)
		}
	}
}

func TestJWT(t *testing.T) {
	in := "auth: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk123"
	if len(Scan(in)) == 0 {
		t.Fatal("jwt not found")
	}
}

func TestNoFalsePositives(t *testing.T) {
	clean := []string{
		"hello world",
		"commit 4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		"password = \"changeme-example-value\"",
		"api_key = \"${API_KEY_FROM_ENV}\"",
		"func main() { fmt.Println(\"x\") }",
		"sha256: " + strings.Repeat("a1", 32),
	}
	for _, c := range clean {
		if fs := Scan(c); len(fs) != 0 {
			t.Errorf("false positive on %q: %+v", c, fs)
		}
	}
}

func TestGenericAssignment(t *testing.T) {
	fs := Scan(`password: "Zk3$hQ9vLp2mXw7r"`)
	if len(fs) == 0 || fs[0].Rule != "generic-assignment" {
		t.Fatalf("got %+v", fs)
	}
	if len(Scan(`password: "aaaaaaaaaaaaaaaa"`)) != 0 {
		t.Fatal("low entropy value flagged")
	}
}

func TestHighEntropy(t *testing.T) {
	tok := "Zq8Xr2Lp9Vm4Nk7Jt3Hd6Gf1Bc5Wy0SaQeRtUiOp"
	fs := Scan("blob=" + tok)
	if len(fs) != 1 || fs[0].Rule != "high-entropy-string" {
		t.Fatalf("got %+v", fs)
	}
}

func TestLineNumbersAndMask(t *testing.T) {
	in := "a\nb\nkey " + "AKIA" + "ABCDEFGHIJKLMNOP\n"
	fs := Scan(in)
	if len(fs) != 1 || fs[0].Line != 3 {
		t.Fatalf("got %+v", fs)
	}
	if strings.Contains(fs[0].Sample, "BCDEFGHIJKLM") {
		t.Fatalf("sample leaks secret: %q", fs[0].Sample)
	}
}

func TestPolicy(t *testing.T) {
	secret := "tok " + fake("ghp_", 40)
	if d := Evaluate(secret, ModeOff, true, false); !d.Allow || len(d.Findings) != 0 {
		t.Fatal("off must allow without scanning")
	}
	if d := Evaluate(secret, ModeWarn, true, false); !d.Allow || len(d.Findings) == 0 {
		t.Fatal("warn must allow and report")
	}
	if d := Evaluate(secret, ModeBlock, true, false); d.Allow || !d.Blocked || !d.CanForce {
		t.Fatalf("block must block: %+v", d)
	}
	if d := Evaluate(secret, ModeBlock, true, true); !d.Allow {
		t.Fatal("user override must allow")
	}
	if d := Evaluate(secret, ModeBlock, false, true); d.Allow || d.CanForce {
		t.Fatal("override must be refused when admin forbids it")
	}
	if d := Evaluate("clean text", ModeBlock, true, false); !d.Allow {
		t.Fatal("clean text blocked")
	}
}
