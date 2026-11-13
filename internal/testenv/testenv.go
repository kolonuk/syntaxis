// SPDX-License-Identifier: GPL-2.0-or-later

// Package testenv loads the private live-test environment (a local LLM server,
// model names, addresses) from a file outside the repository, so none of it is
// ever committed. Live tests skip when the file is absent.
package testenv

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path returns the env file location (SYNTAXIS_TEST_ENV or ~/.config/syntaxis-test/env).
func Path() string {
	if p := os.Getenv("SYNTAXIS_TEST_ENV"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "syntaxis-test", "env")
}

// Load returns KEY=VALUE pairs. Live tests are opt-in (SYNTAXIS_LIVE=1) so a
// routine `go test ./...` never touches the local LLM; they also skip when the
// file does not exist.
func Load(t testing.TB) map[string]string {
	t.Helper()
	if os.Getenv("SYNTAXIS_LIVE") != "1" {
		t.Skip("live tests are opt-in: set SYNTAXIS_LIVE=1")
	}
	f, err := os.Open(Path())
	if err != nil {
		t.Skip("no private test environment (" + Path() + ")")
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
