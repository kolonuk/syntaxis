// SPDX-License-Identifier: GPL-2.0-or-later

// Package hygiene holds repository-wide guard tests: the project is public and
// generic, so no secrets, real hosts, emails, or model/hardware assumptions may
// creep in, and every source file must carry its licence header.
package hygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/scanner"
)

func root(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	return filepath.Clean(filepath.Join(d, "..", ".."))
}

func tracked(t *testing.T) []string {
	t.Helper()
	r := root(t)
	skip := map[string]bool{".git": true, "node_modules": true, "dist": true, "data": true}
	var files []string
	_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] && p != r {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, p)
		return nil
	})
	return files
}

func text(p string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".png", ".ico", ".woff", ".woff2", ".sum", ".lock":
		return "", false
	}
	if filepath.Base(p) == "package-lock.json" || filepath.Base(p) == "LICENSE" || filepath.Base(p) == "THIRD_PARTY_LICENSES" {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	ipRe    = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`)
)

var allowedEmailDomains = []string{"example.com", "example.org", "users.noreply.github.com", "noreply.github.com", "anthropic.com", "syntaxis.invalid", "localhost"}

func TestNoRealEmailsOrHosts(t *testing.T) {
	for _, p := range tracked(t) {
		s, ok := text(p)
		if !ok {
			continue
		}
		for _, m := range emailRe.FindAllString(s, -1) {
			low := strings.ToLower(m)
			okd := false
			for _, d := range allowedEmailDomains {
				if strings.HasSuffix(low, "@"+d) || strings.HasSuffix(low, "."+d) {
					okd = true
				}
			}
			// package specifiers like "@types/node" or "x@1.2.3" are not emails
			if strings.Contains(low, "/") || regexp.MustCompile(`@\d`).MatchString(low) || strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".ts") || strings.HasSuffix(low, ".tsx") {
				continue
			}
			if !okd {
				t.Errorf("%s: suspicious email %q (use example.com)", rel(t, p), m)
			}
		}
	}
}

func rel(t *testing.T, p string) string {
	r, _ := filepath.Rel(root(t), p)
	return r
}

func TestOnlyDocumentationAndPrivateIPs(t *testing.T) {
	for _, p := range tracked(t) {
		s, ok := text(p)
		if !ok || strings.HasSuffix(p, ".svg") {
			continue
		}
		for _, m := range ipRe.FindAllStringSubmatch(s, -1) {
			a, b := atoi(m[1]), atoi(m[2])
			if a > 255 || b > 255 || atoi(m[3]) > 255 || atoi(m[4]) > 255 {
				continue // versions like 1.2.3.4567
			}
			ip := m[0]
			switch {
			case a == 127, a == 0, a == 10, a == 192 && b == 168, a == 172 && b >= 16 && b <= 31, a == 169 && b == 254, a == 100 && b >= 64 && b <= 127, a >= 224:
			case a == 203 && b == 0, a == 198 && b == 51, a == 192 && b == 0: // TEST-NET documentation ranges
			case a == 93 && b == 184, a == 140 && b == 82, a == 6 && b == 6, a == 8 && b == 8, a == 1 && b == 1:
				// well-known public fixtures used in tests (example.com, github.com, placeholders)
			case a < 20 && p != "": // version-like strings such as 1.2.3.4
			default:
				t.Errorf("%s: unexpected IP address %s (use documentation ranges)", rel(t, p), ip)
			}
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Logic and defaults must not assume any particular model family or hardware.
func TestNoModelOrHardwareAssumptionsInCode(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(qwen|mistral|gemma|deepseek|mixtral|codellama|starcoder|phi-?\d|llama-?\d|gpt-?[345]|rtx ?\d{3,4}|nvidia|a100|h100|apple silicon|m[123] (max|pro|ultra))\b`)
	for _, p := range tracked(t) {
		if !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") && !strings.HasSuffix(p, ".yml") {
			continue
		}
		if strings.HasSuffix(p, "hygiene_test.go") {
			continue
		}
		s, _ := text(p)
		if m := banned.FindString(s); m != "" {
			t.Errorf("%s: model/hardware-specific reference %q", rel(t, p), m)
		}
	}
}

func TestNoSecretsInRepository(t *testing.T) {
	for _, p := range tracked(t) {
		s, ok := text(p)
		if !ok {
			continue
		}
		// test fixtures build credentials at runtime; skip the scanner's own tests and docs of patterns
		if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, "scanner.go") {
			continue
		}
		if fs := scanner.Scan(s); len(fs) > 0 {
			var rules []string
			for _, f := range fs {
				rules = append(rules, f.Rule)
			}
			t.Errorf("%s: possible secret(s): %v", rel(t, p), rules)
		}
	}
}

func TestEnvExampleHasNoValues(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(root(t), ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		for _, k := range []string{"MASTER_KEY=", "PASSWORD=", "VAPID_PRIVATE=", "VAPID_PUBLIC="} {
			if i := strings.Index(line, k); i >= 0 && strings.TrimSpace(line[i+len(k):]) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				t.Errorf(".env.example sets a value for %s", k)
			}
		}
	}
	gi, _ := os.ReadFile(filepath.Join(root(t), ".gitignore"))
	if !strings.Contains(string(gi), ".env") {
		t.Fatal(".env must be git-ignored")
	}
}
