// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/audit"
)

func TestKeygen(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"keygen"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
	if err != nil || len(k) != 32 {
		t.Fatalf("%q %v", out.String(), err)
	}
	// The VAPID flag went away with push notifications: asking for it is an error, not a silent key.
	out.Reset()
	if err := run([]string{"keygen", "--vapid"}, &out, &out); err == nil {
		t.Fatalf("keygen --vapid still accepted: %q", out.String())
	}
}

func TestUsageAndUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if err := run(nil, &out, &errb); err == nil {
		t.Fatal("no command accepted")
	}
	if err := run([]string{"frobnicate"}, &out, &errb); err == nil {
		t.Fatal("unknown command accepted")
	}
	out.Reset()
	if err := run([]string{"version"}, &out, &errb); err != nil || strings.TrimSpace(out.String()) != version() {
		t.Fatal("version")
	}
	if err := run([]string{"help"}, &out, &errb); err != nil {
		t.Fatal("help")
	}
}

func TestAdminCreateAndBackup(t *testing.T) {
	dir := t.TempDir()
	var key bytes.Buffer
	_ = run([]string{"keygen"}, &key, &key)
	t.Setenv("SYNTAXIS_DATA_DIR", dir)
	t.Setenv("SYNTAXIS_MASTER_KEY", strings.TrimSpace(key.String()))
	t.Setenv("SYNTAXIS_NEW_ADMIN_PASSWORD", "correct horse battery staple")
	var out bytes.Buffer
	if err := run([]string{"admin", "create", "boss@example.com", "Boss Person"}, &out, &out); err != nil || !strings.Contains(out.String(), "created admin") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := run([]string{"admin", "create", "boss@example.com", "Dup"}, &out, &out); err == nil {
		t.Fatal("duplicate admin accepted")
	}
	t.Setenv("SYNTAXIS_NEW_ADMIN_PASSWORD", "weak")
	if err := run([]string{"admin", "create", "x@example.com", "X"}, &out, &out); err == nil {
		t.Fatal("weak admin password accepted")
	}
	if err := run([]string{"admin", "create"}, &out, &out); err == nil {
		t.Fatal("missing args")
	}
	dest := filepath.Join(t.TempDir(), "backup.db")
	out.Reset()
	if err := run([]string{"backup", dest}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal("no backup file")
	}
	if !strings.Contains(out.String(), "master key") {
		t.Fatal("backup output must remind about the master key")
	}
}

func TestAdminSetupLinkCommand(t *testing.T) {
	var key bytes.Buffer
	_ = run([]string{"keygen"}, &key, &key)
	t.Setenv("SYNTAXIS_DATA_DIR", t.TempDir())
	t.Setenv("SYNTAXIS_MASTER_KEY", strings.TrimSpace(key.String()))
	t.Setenv("SYNTAXIS_ADMIN_URL", "https://admin.example.com/")
	var out bytes.Buffer
	if err := run([]string{"admin", "setup-link", "first@example.com", "First"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://admin.example.com/#setup=") {
		t.Fatalf("%q", out.String())
	}
}

func TestAdminCreateNeedsMasterKey(t *testing.T) {
	t.Setenv("SYNTAXIS_DATA_DIR", t.TempDir())
	t.Setenv("SYNTAXIS_MASTER_KEY", "")
	t.Setenv("SYNTAXIS_MASTER_KEY_FILE", "")
	var out bytes.Buffer
	if err := run([]string{"admin", "create", "a@example.com", "A"}, &out, &out); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("%v", err)
	}
}

func TestRotateKeyCommand(t *testing.T) {
	dir := t.TempDir()
	var k1, k2 bytes.Buffer
	_ = run([]string{"keygen"}, &k1, &k1)
	_ = run([]string{"keygen"}, &k2, &k2)
	f1, f2 := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	_ = os.WriteFile(f1, k1.Bytes(), 0o600)
	_ = os.WriteFile(f2, k2.Bytes(), 0o600)
	t.Setenv("SYNTAXIS_DATA_DIR", dir)
	var out bytes.Buffer
	if err := run([]string{"rotate-key", f1, f2}, &out, &out); err != nil || !strings.Contains(out.String(), "re-encrypted 0 secrets") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := run([]string{"rotate-key", f1}, &out, &out); err == nil {
		t.Fatal("args")
	}
}

func TestEgressRulesCommand(t *testing.T) {
	t.Setenv("SYNTAXIS_EGRESS_MODE", "open")
	var out bytes.Buffer
	if err := run([]string{"egress-rules", "172.30.0.0/24", "172.30.0.2", "172.30.0.1", "203.0.113.5/32"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"table inet syntaxis_sandbox", "169.254.0.0/16", "203.0.113.5/32", "tcp dport { 25, 465, 587 }"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if err := run([]string{"egress-rules", "nonsense"}, &out, &out); err == nil {
		t.Fatal("bad subnet accepted")
	}
	if err := run([]string{"egress-rules"}, &out, &out); err == nil {
		t.Fatal("missing args")
	}
	t.Setenv("SYNTAXIS_EGRESS_MODE", "allowlist")
	t.Setenv("SYNTAXIS_EGRESS_ALLOWLIST", "")
	if err := run([]string{"egress-rules", "172.30.0.0/24"}, &out, &out); err == nil {
		t.Fatal("allowlist mode with an empty list must fail")
	}
}

func TestAuditVerifyCommand(t *testing.T) {
	var log bytes.Buffer
	prev := audit.GenesisHash
	var es []audit.Entry
	for i := 1; i <= 3; i++ {
		e := audit.Seal(audit.Entry{Seq: int64(i), Time: time.Unix(int64(i), 0), Actor: "admin:1", Action: "a", Target: "t"}, prev)
		es = append(es, e)
		prev = e.Hash
	}
	_ = audit.WriteLines(&log, es)
	file := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(file, log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdAudit([]string{"verify", file}, nil, &out); err != nil {
		t.Fatalf("an intact export failed: %v", err)
	}
	if !strings.Contains(out.String(), "3 entries") || !strings.Contains(out.String(), es[2].Hash) {
		t.Fatalf("the output must give the length and the head hash: %q", out.String())
	}
	// stdin works too
	out.Reset()
	if err := cmdAudit([]string{"verify", "-"}, bytes.NewReader(log.Bytes()), &out); err != nil || !strings.Contains(out.String(), "3 entries") {
		t.Fatalf("stdin: %v %q", err, out.String())
	}

	// a changed entry is reported with where
	bad := strings.Replace(log.String(), `"action":"a"`, `"action":"b"`, 1)
	if err := os.WriteFile(file, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdAudit([]string{"verify", file}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "NOT intact") || !strings.Contains(err.Error(), "entry 1") {
		t.Fatalf("a changed log must fail and say where: %v", err)
	}
	if err := cmdAudit([]string{"verify", filepath.Join(t.TempDir(), "missing")}, nil, &out); err == nil {
		t.Fatal("a missing file passed")
	}
	if err := cmdAudit([]string{"explode"}, nil, &out); err == nil {
		t.Fatal("an unknown subcommand passed")
	}
}
