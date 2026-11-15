// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSync runs scripts/firewall-sync.sh --once against a state file with a stub nft that
// records what it is given.
func runSync(t *testing.T, state string) (script, stderr string) {
	t.Helper()
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "nft.in")
	stub := filepath.Join(dir, "nft")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := filepath.Join(dir, "state")
	if err := os.WriteFile(st, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "../../scripts/firewall-sync.sh", "--once")
	cmd.Env = append(os.Environ(), "FW_STATE_FILE="+st, "NFT_BIN="+stub, "FW_TABLE=syntaxis_sandbox")
	var eb strings.Builder
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, eb.String())
	}
	b, _ := os.ReadFile(out)
	return string(b), eb.String()
}

func TestFirewallSyncBuildsSetUpdates(t *testing.T) {
	script, _ := runSync(t, FormatState(
		[]SandboxEntry{{IP: ip("172.30.0.5"), Mode: ModeAllowlist}, {IP: ip("172.30.0.7"), Mode: ModeOpen}, {IP: ip("172.30.0.9"), Mode: ModeNone}, {IP: ip("172.30.0.10"), Mode: ModeNone}},
		[]HostEntry{{IP: ip("93.184.216.34"), TTL: 90e9}},
	))
	for _, want := range []string{
		"flush set inet syntaxis_sandbox sandbox_none\n",
		"add element inet syntaxis_sandbox sandbox_none { 172.30.0.9, 172.30.0.10 }\n",
		"add element inet syntaxis_sandbox sandbox_allow { 172.30.0.5 }\n",
		"add element inet syntaxis_sandbox sandbox_open { 172.30.0.7 }\n",
		"flush set inet syntaxis_sandbox allowed_hosts\n",
		"add element inet syntaxis_sandbox allowed_hosts { 93.184.216.34 timeout 90s }\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %q in\n%s", want, script)
		}
	}
}

func TestFirewallSyncEmptyStateClearsTheSets(t *testing.T) {
	script, _ := runSync(t, "")
	if strings.Contains(script, "add element") || !strings.Contains(script, "flush set inet syntaxis_sandbox sandbox_allow") {
		t.Fatalf("%s", script)
	}
}

func TestFirewallSyncNeverPassesThroughAnythingButValidLines(t *testing.T) {
	// a compromised Syntaxis must not be able to get arbitrary text into nft
	script, stderr := runSync(t, strings.Join([]string{
		"sandbox 172.30.0.5 allow",
		"flush ruleset",
		"sandbox 172.30.0.6 allow; flush ruleset",
		"sandbox 172.30.0.999 open",
		"sandbox 1.2.3.4/8 open",
		"host 93.184.216.34 99999999",
		"host 5.6.7.8 90 extra",
		"delete table inet syntaxis_sandbox",
		"sandbox $(reboot) none",
	}, "\n"))
	for _, line := range strings.Split(script, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "flush set inet syntaxis_sandbox ") && !strings.HasPrefix(line, "add element inet syntaxis_sandbox ") {
			t.Fatalf("unexpected nft line %q", line)
		}
	}
	for _, bad := range []string{"flush ruleset", "172.30.0.999", "1.2.3.4/8", "reboot", "delete table", "5.6.7.8", "172.30.0.6"} {
		if strings.Contains(script, bad) {
			t.Errorf("%q reached nft:\n%s", bad, script)
		}
	}
	if !strings.Contains(script, "sandbox_allow { 172.30.0.5 }") || !strings.Contains(script, "93.184.216.34 timeout 3600s") {
		t.Errorf("valid lines lost or TTL not capped:\n%s", script)
	}
	if !strings.Contains(stderr, "skipped") {
		t.Errorf("invalid lines should be reported: %q", stderr)
	}
}
