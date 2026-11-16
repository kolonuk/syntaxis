// SPDX-License-Identifier: GPL-2.0-or-later

// Package testenv loads the private live-test environment (a local LLM server,
// model names, addresses) from a file outside the repository, so none of it is
// ever committed. Live tests skip when the file is absent.
package testenv

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		NotTested(t, "live tests are opt-in: set SYNTAXIS_LIVE=1 (they use the private test environment)")
	}
	f, err := os.Open(Path())
	if err != nil {
		NotTested(t, "no private test environment file")
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

// NotTested skips the test and says so in a form scripts/not-tested.sh can collect. Use it whenever a
// test cannot run for lack of a real service, never to stand in a pretend one.
func NotTested(t testing.TB, reason string) {
	t.Helper()
	t.Skip("NOT TESTED: " + reason)
}

// Network skips the test unless the real internet host is reachable.
func Network(t testing.TB, hostport string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", hostport, 5*time.Second)
	if err != nil {
		NotTested(t, hostport+" is not reachable from here: "+err.Error())
	}
	_ = c.Close()
}

// Env returns a credential the test needs from the environment, or skips saying which one is missing.
func Env(t testing.TB, name, what string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		NotTested(t, what+" needs a real account: set "+name+" to run it")
	}
	return v
}

// Docker skips unless a real Docker engine answers. The engine's own client is the source of truth.
func Docker(t testing.TB) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		NotTested(t, "no Docker engine is reachable")
	}
}

// GVisor skips unless Docker has the runsc runtime registered.
func GVisor(t testing.TB) {
	t.Helper()
	Docker(t)
	out, err := exec.Command("docker", "info", "--format", "{{json .Runtimes}}").Output()
	if err != nil || !strings.Contains(string(out), "runsc") {
		NotTested(t, "gVisor (runsc) is not registered with Docker")
	}
}

// KeepDNSVolume remembers the resolver file in Syntaxis's shared DNS volume and puts it back when
// the test ends. Tests that start sandboxes with a resolver rewrite that file, and another
// Syntaxis on the same Docker host only writes it when it starts, so it must not be left changed.
func KeepDNSVolume(t testing.TB, image string) {
	t.Helper()
	const vol = "syntaxis-dns"
	existed := exec.Command("docker", "volume", "inspect", vol).Run() == nil
	var before []byte
	if existed {
		before, _ = exec.Command("docker", "run", "--rm", "--user", "0", "-v", vol+":/d", image, "sh", "-c", "cat /d/resolv.conf 2>/dev/null").Output()
	}
	t.Cleanup(func() {
		if !existed {
			_ = exec.Command("docker", "volume", "rm", "-f", vol).Run()
			return
		}
		if len(before) > 0 {
			cmd := exec.Command("docker", "run", "--rm", "-i", "--user", "0", "-v", vol+":/d", image, "sh", "-c", "cat > /d/resolv.conf")
			cmd.Stdin = strings.NewReader(string(before))
			_ = cmd.Run()
		}
	})
}
