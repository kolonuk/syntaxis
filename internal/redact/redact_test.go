// SPDX-License-Identifier: GPL-2.0-or-later

package redact

import (
	"strings"
	"testing"
)

func TestKnownValues(t *testing.T) {
	r := New("super-secret-value", "abc")
	got := r.String("the token is super-secret-value ok abc")
	if strings.Contains(got, "super-secret-value") {
		t.Fatalf("value leaked: %q", got)
	}
	if !strings.Contains(got, "abc") {
		t.Fatalf("short value should be skipped: %q", got)
	}
}

func TestPatternRedaction(t *testing.T) {
	in := "export TOKEN=" + "ghp_" + strings.Repeat("aB3dE5gH7j", 4) + " done"
	got := New().String(in)
	if strings.Contains(got, "ghp_") || !strings.Contains(got, Mask) || !strings.HasSuffix(got, " done") {
		t.Fatalf("bad redaction: %q", got)
	}
}

func TestLongestFirst(t *testing.T) {
	r := New("secretvalue", "secretvalue-extended")
	if got := r.String("x secretvalue-extended y"); strings.Contains(got, "extended") {
		t.Fatalf("partial redaction left suffix: %q", got)
	}
}

func TestCleanUnchanged(t *testing.T) {
	if got := New("x123456").String("nothing here"); got != "nothing here" {
		t.Fatalf("changed: %q", got)
	}
}
