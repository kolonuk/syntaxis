// SPDX-License-Identifier: GPL-2.0-or-later

// Package redact removes secrets from text before it reaches logs or a model.
package redact

import (
	"sort"
	"strings"

	"github.com/kolonuk/syntaxis/internal/scanner"
)

// Mask replaces redacted spans.
const Mask = "[REDACTED]"

// minValueLen avoids redacting trivially short strings that would shred output.
const minValueLen = 6

// Redactor holds known secret values (for example a user's stored tokens) in
// addition to the pattern-based scanner. Best-effort only: see docs/security.md.
type Redactor struct {
	values []string
}

// New builds a Redactor for the given exact secret values.
func New(values ...string) *Redactor {
	r := &Redactor{}
	r.Add(values...)
	return r
}

// Add registers more secret values. Longer values are replaced first.
func (r *Redactor) Add(values ...string) {
	for _, v := range values {
		if len(v) >= minValueLen {
			r.values = append(r.values, v)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// String redacts known values and pattern matches.
func (r *Redactor) String(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Mask)
	}
	fs := scanner.Scan(s)
	if len(fs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, f := range fs {
		if f.Start < last {
			continue
		}
		b.WriteString(s[last:f.Start])
		b.WriteString(Mask)
		last = f.End
	}
	b.WriteString(s[last:])
	return b.String()
}
