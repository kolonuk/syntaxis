// SPDX-License-Identifier: GPL-2.0-or-later

package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.EgressMode != "open" || c.AdminReadMsg || c.Metrics || c.ProxyAuth.Enabled {
		t.Fatalf("unsafe defaults: %+v", c)
	}
	if c.AdminListen != "127.0.0.1:8081" {
		t.Fatalf("admin listen default %q", c.AdminListen)
	}
	if c.PasswordRotationDays != 30 || c.IdleTimeout != 30*time.Minute {
		t.Fatalf("policy defaults wrong")
	}
	if !c.DevMailMode() {
		t.Fatal("expected dev mail mode")
	}
}

func TestInvalidEnums(t *testing.T) {
	for k, v := range map[string]string{
		"SYNTAXIS_EGRESS_MODE":  "wide",
		"SYNTAXIS_SECRET_SCAN":  "maybe",
		"SYNTAXIS_SIGNING_MODE": "always",
	} {
		if _, err := LoadFrom(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
}

func TestBadInteger(t *testing.T) {
	if _, err := LoadFrom(env(map[string]string{"SYNTAXIS_SMTP_PORT": "x"})); err == nil {
		t.Fatal("expected error")
	}
	if _, err := LoadFrom(env(map[string]string{"SYNTAXIS_PASSWORD_ROTATION_DAYS": "-1"})); err == nil {
		t.Fatal("expected error for negative")
	}
}

func TestMasterKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	c, err := LoadFrom(env(map[string]string{"SYNTAXIS_MASTER_KEY": good}))
	if err != nil || len(c.MasterKey) != 32 {
		t.Fatalf("err=%v len=%d", err, len(c.MasterKey))
	}
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := LoadFrom(env(map[string]string{"SYNTAXIS_MASTER_KEY": short})); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := LoadFrom(env(map[string]string{"SYNTAXIS_MASTER_KEY": "!!!"})); err == nil {
		t.Fatal("bad base64 accepted")
	}
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, []byte(good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadFrom(env(map[string]string{"SYNTAXIS_MASTER_KEY_FILE": p}))
	if err != nil || len(c.MasterKey) != 32 {
		t.Fatalf("file key: %v", err)
	}
	if _, err := LoadFrom(env(map[string]string{"SYNTAXIS_MASTER_KEY_FILE": "/nonexistent/x"})); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestListsAndBools(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"SYNTAXIS_EGRESS_ALLOWLIST": " a.example.com, ,b.example.com ",
		"SYNTAXIS_PROXY_TRUSTED":    "10.0.0.1",
		"SYNTAXIS_METRICS_ENABLED":  "YES",
		"SYNTAXIS_BASE_URL":         "https://x.example.com/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.EgressAllow, "|") != "a.example.com|b.example.com" {
		t.Fatalf("allowlist %v", c.EgressAllow)
	}
	if !c.Metrics || c.BaseURL != "https://x.example.com" || len(c.ProxyAuth.Trusted) != 1 {
		t.Fatalf("parsed wrong: %+v", c)
	}
}
