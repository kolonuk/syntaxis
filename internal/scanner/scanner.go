// SPDX-License-Identifier: GPL-2.0-or-later

// Package scanner is a lightweight, built-in secret scanner. The rule set is
// original (provider key prefixes and shapes are public facts) and is not
// copied from any third-party rule file.
package scanner

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Finding is one suspected secret.
type Finding struct {
	Rule   string `json:"rule"`
	Line   int    `json:"line"`
	Start  int    `json:"start"` // byte offset within the input
	End    int    `json:"end"`
	Sample string `json:"sample"` // masked preview, never the full secret
}

type rule struct {
	id string
	re *regexp.Regexp
	// group is the capture group holding the secret (0 = whole match).
	group int
}

var rules = []rule{
	{"aws-access-key-id", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[A-Z0-9]{16}\b`), 0},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["'\s:=]+([A-Za-z0-9/+=]{40})\b`), 1},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`), 0},
	{"github-fine-grained-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`), 0},
	{"gitlab-pat", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), 0},
	{"stripe-secret-key", regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}\b`), 0},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), 0},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), 0},
	{"cloudflare-api-token", regexp.MustCompile(`(?i)cloudflare[_\- ]?(?:api[_\- ]?)?token["'\s:=]+([A-Za-z0-9_\-]{40})\b`), 1},
	{"generic-assignment", regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token)["']?\s*[:=]\s*["']([^"'\s]{12,})["']`), 1},
}

var placeholderWords = []string{"example", "changeme", "placeholder", "your_", "your-", "xxxx", "<", "${", "{{", "dummy", "redacted"}

// Scan returns findings in text, sorted by position.
func Scan(text string) []Finding {
	var out []Finding
	lineStarts := lineIndex(text)
	for _, r := range rules {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			s, e := m[0], m[1]
			if r.group > 0 {
				if m[2*r.group] < 0 {
					continue
				}
				s, e = m[2*r.group], m[2*r.group+1]
			}
			val := text[s:e]
			if r.id == "generic-assignment" && (isPlaceholder(val) || entropy(val) < 3.0) {
				continue
			}
			out = append(out, Finding{Rule: r.id, Line: lineOf(lineStarts, s), Start: s, End: e, Sample: mask(val)})
		}
	}
	out = append(out, entropyFindings(text, lineStarts, out)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return dedupe(out)
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9+/_\-=]{32,}`)

// entropyFindings flags long, high-entropy tokens not already covered.
func entropyFindings(text string, ls []int, existing []Finding) []Finding {
	var out []Finding
	for _, m := range tokenRe.FindAllStringIndex(text, -1) {
		val := text[m[0]:m[1]]
		if covered(existing, m[0], m[1]) || isPlaceholder(val) || looksLikeHashOrPath(val) {
			continue
		}
		if entropy(val) >= 4.5 && hasMixedClasses(val) {
			out = append(out, Finding{Rule: "high-entropy-string", Line: lineOf(ls, m[0]), Start: m[0], End: m[1], Sample: mask(val)})
		}
	}
	return out
}

func covered(fs []Finding, s, e int) bool {
	for _, f := range fs {
		if s < f.End && e > f.Start {
			return true
		}
	}
	return false
}

func dedupe(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if n := len(out); n > 0 && f.Start < out[n-1].End {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isPlaceholder(s string) bool {
	l := strings.ToLower(s)
	for _, w := range placeholderWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// looksLikeHashOrPath filters pure-hex digests (git SHAs, checksums).
func looksLikeHashOrPath(s string) bool {
	hex := true
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			hex = false
			break
		}
	}
	return hex
}

func hasMixedClasses(s string) bool {
	var lo, up, dg bool
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= '0' && c <= '9':
			dg = true
		}
	}
	n := 0
	for _, b := range []bool{lo, up, dg} {
		if b {
			n++
		}
	}
	return n >= 3
}

func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	n := 0.0
	for _, c := range s {
		freq[c]++
		n++
	}
	h := 0.0
	for _, f := range freq {
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}

func mask(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", 4) + s[len(s)-2:]
}

func lineIndex(text string) []int {
	idx := []int{0}
	for i, c := range text {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func lineOf(starts []int, off int) int {
	return sort.Search(len(starts), func(i int) bool { return starts[i] > off })
}

// Mode is the scan policy.
type Mode string

// Policy modes.
const (
	ModeOff   Mode = "off"
	ModeWarn  Mode = "warn"
	ModeBlock Mode = "block"
)

// Decision is the outcome of applying a policy to findings.
type Decision struct {
	Allow    bool
	Blocked  bool
	CanForce bool // user may override with explicit confirmation
	Findings []Finding
	Message  string
}

// Evaluate applies policy to text. force is the user's explicit override.
func Evaluate(text string, mode Mode, allowUserOverride, force bool) Decision {
	if mode == ModeOff {
		return Decision{Allow: true}
	}
	fs := Scan(text)
	if len(fs) == 0 {
		return Decision{Allow: true}
	}
	d := Decision{Findings: fs, CanForce: allowUserOverride}
	switch {
	case mode == ModeWarn:
		d.Allow = true
		d.Message = "possible secrets found; proceeding because policy is warn"
	case force && allowUserOverride:
		d.Allow = true
		d.Message = "possible secrets found; user override recorded"
	default:
		d.Blocked = true
		d.Message = "push blocked: possible secrets in the diff"
		if allowUserOverride {
			d.Message += " (you may confirm to push anyway)"
		}
	}
	return d
}
