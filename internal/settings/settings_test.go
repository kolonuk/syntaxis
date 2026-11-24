// SPDX-License-Identifier: GPL-2.0-or-later

package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/store"
)

// memSecrets keeps write-only values in memory; the real service encrypts them, and the server tests
// run against that one.
type memSecrets map[string]string

func (m memSecrets) Has(_ context.Context, n string) bool           { _, ok := m[n]; return ok }
func (m memSecrets) Put(_ context.Context, n, v string) error       { m[n] = v; return nil }
func (m memSecrets) Delete(_ context.Context, n string) error       { delete(m, n); return nil }
func (m memSecrets) Get(_ context.Context, n string) (string, bool) { v, ok := m[n]; return v, ok }

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func find(items []Item, key string) Item {
	for _, it := range items {
		if it.Key == key {
			return it
		}
	}
	return Item{}
}

func TestEveryOptionIsWellFormed(t *testing.T) {
	seenKey, seenEnv := map[string]bool{}, map[string]bool{}
	groups := map[string]bool{}
	for _, g := range Groups {
		groups[g.ID] = true
	}
	for _, d := range Defs {
		if d.Key == "" || d.Label == "" || !groups[d.Group] {
			t.Errorf("%+v is missing a key, label or a known group", d)
		}
		if seenKey[d.Key] || (d.Env != "" && seenEnv[d.Env]) {
			t.Errorf("%s / %s is listed twice", d.Key, d.Env)
		}
		seenKey[d.Key], seenEnv[d.Env] = true, true
		if d.Kind == Choice && len(d.Options) == 0 {
			t.Errorf("%s is a choice with no options", d.Key)
		}
		if d.Locked && (d.Apply != nil || d.Live) {
			t.Errorf("%s belongs to the deployment, so it cannot be applied or live", d.Key)
		}
		if !d.Locked && d.Kind != Secret && !d.Live && d.Apply == nil {
			t.Errorf("%s can be saved but nothing applies it", d.Key)
		}
		// a default must itself be acceptable
		if d.Default != "" && !d.Locked && d.Kind != Secret && d.Validate != nil {
			if err := d.Validate(d.Default); err != nil {
				t.Errorf("%s: its own default %q is refused: %v", d.Key, d.Default, err)
			}
		}
	}
	// the options that were already saved in the database keep their keys
	for _, k := range []string{"password_rotation_days", "secret_scan_mode", "secret_scan_allow_user_override"} {
		if !seenKey[k] {
			t.Errorf("%s changed its key, so a value saved earlier would be lost", k)
		}
	}
}

// Every environment variable the configuration reads is either an option here or deliberately
// first-boot only, so nothing a deployment sets is missing from the admin app.
func TestEveryConfigurationVariableIsAccountedFor(t *testing.T) {
	firstBootOnly := map[string]bool{
		"SYNTAXIS_MASTER_KEY_FILE": true, "SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL": true, "SYNTAXIS_BOOTSTRAP_ADMIN_PASSWORD": true,
		"SYNTAXIS_BOOTSTRAP_ADMIN_ALLOW_SHORT_PASSWORD": true, "SYNTAXIS_BOOTSTRAP_PROVIDER_URL": true, "SYNTAXIS_BOOTSTRAP_PROVIDER_NAME": true,
		"SYNTAXIS_BOOTSTRAP_PROVIDER_KIND": true, "SYNTAXIS_BOOTSTRAP_PROVIDER_CONCURRENCY": true, "SYNTAXIS_BOOTSTRAP_PROVIDER_ENABLE_MODELS": true,
	}
	var asked []string
	_, _ = config.LoadFrom(func(k string) string { asked = append(asked, k); return "" })
	known := map[string]bool{}
	for _, d := range Defs {
		known[d.Env] = true
	}
	for _, k := range asked {
		if !known[k] && !firstBootOnly[k] {
			t.Errorf("%s is read from the environment but is not in the admin app's list of options", k)
		}
	}
}

func TestSourceIsTheDatabaseThenTheEnvironmentThenTheDefault(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	sec := memSecrets{}
	e := env(map[string]string{"SYNTAXIS_SMTP_HOST": "mail.docker.example", "SYNTAXIS_IDLE_MINUTES": "45"})

	items := Resolve(ctx, st, sec, e)
	if it := find(items, "smtp_host"); it.Source != SourceDocker || it.Value != "mail.docker.example" || it.EnvSet != true {
		t.Fatalf("an environment value is reported as docker: %+v", it)
	}
	if it := find(items, "smtp_port"); it.Source != SourceDefault || it.Value != "587" {
		t.Fatalf("nothing set is the default: %+v", it)
	}
	if it := find(items, "idle_minutes"); it.Value != "45" || it.Source != SourceDocker {
		t.Fatalf("%+v", it)
	}

	// saving in the app replaces the environment's value, and remembers what the environment said
	_ = st.SetSetting(ctx, "smtp_host", "mail.saved.example")
	if it := find(Resolve(ctx, st, sec, e), "smtp_host"); it.Source != SourceDatabase || it.Value != "mail.saved.example" || it.EnvValue != "mail.docker.example" {
		t.Fatalf("a saved value replaces the environment's: %+v", it)
	}
	if got := Effective(ctx, st, e, "smtp_host"); got != "mail.saved.example" {
		t.Fatalf("effective %q", got)
	}
	// removing it puts the environment's back
	_ = st.DeleteSetting(ctx, "smtp_host")
	if it := find(Resolve(ctx, st, sec, e), "smtp_host"); it.Source != SourceDocker || it.Value != "mail.docker.example" {
		t.Fatalf("%+v", it)
	}
	// a saved empty value is a real choice ("send no mail"), not "unset"
	_ = st.SetSetting(ctx, "smtp_host", "")
	if it := find(Resolve(ctx, st, sec, e), "smtp_host"); it.Source != SourceDatabase || it.Value != "" {
		t.Fatalf("an empty saved value must stay saved: %+v", it)
	}
}

func TestSecretsAreNeverReturnedAndTheirSourceIsKnown(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	sec := memSecrets{}
	it := find(Resolve(ctx, st, sec, env(nil)), "smtp_password")
	if it.HasValue || it.Source != SourceDefault {
		t.Fatalf("%+v", it)
	}
	it = find(Resolve(ctx, st, sec, env(map[string]string{"SYNTAXIS_SMTP_PASSWORD": "from-docker"})), "smtp_password")
	if !it.HasValue || it.Source != SourceDocker || it.Value != "" || it.EnvValue != "" {
		t.Fatalf("the environment's password must not be echoed: %+v", it)
	}
	_ = sec.Put(ctx, SecretName("smtp_password"), "saved-secret")
	it = find(Resolve(ctx, st, sec, env(map[string]string{"SYNTAXIS_SMTP_PASSWORD": "from-docker"})), "smtp_password")
	if it.Source != SourceDatabase || it.Value != "" {
		t.Fatalf("%+v", it)
	}
	for _, x := range Resolve(ctx, st, sec, env(map[string]string{"SYNTAXIS_MASTER_KEY": "k", "SYNTAXIS_FIREWALL_TOKEN": "t"})) {
		if x.Kind == Secret && (x.Value != "" || x.EnvValue != "") {
			t.Errorf("%s leaks a value: %+v", x.Key, x)
		}
		if (x.Key == "master_key" || x.Key == "firewall_token") && (x.Value != "" || !x.HasValue || !x.Locked) {
			t.Errorf("%s must show only that it is set: %+v", x.Key, x)
		}
	}
	if !ReservedSecret(SecretName("smtp_password")) || ReservedSecret("MY_API_KEY") {
		t.Fatal("reserved names")
	}
}

func TestLockedOptionsCannotBeSavedAndSavedValuesAreChecked(t *testing.T) {
	for _, key := range []string{"listen", "data_dir", "docker_host", "master_key", "firewall_token", "sandbox_image"} {
		if err := Validate(key, "x"); err == nil || !strings.Contains(err.Error(), "deployment") {
			t.Errorf("%s was accepted: %v", key, err)
		}
	}
	for _, tc := range []struct {
		key, value string
		ok         bool
	}{
		{"smtp_port", "587", true}, {"smtp_port", "0", false}, {"smtp_port", "70000", false}, {"smtp_port", "abc", false},
		{"smtp_host", "mail.example.com", true}, {"smtp_host", "mail.example.com/x", false}, {"smtp_host", "a b", false}, {"smtp_host", "", true},
		{"smtp_from", "a@example.com", true}, {"smtp_from", "a@example.com\nBcc: x@y", false},
		{"base_url", "https://x.example.com", true}, {"base_url", "x.example.com", false}, {"base_url", "javascript:alert(1)", false},
		{"egress_mode", "open", true}, {"egress_mode", "everything", false},
		{"secret_scan_mode", "block", true}, {"secret_scan_mode", "maybe", false},
		{"secret_scan_allow_user_override", "false", true}, {"secret_scan_allow_user_override", "yes", false},
		{"password_rotation_days", "0", true}, {"password_rotation_days", "-1", false},
		{"idle_minutes", "30", true}, {"idle_minutes", "soon", false},
		{"connections_allow_private", "192.0.2.0/24", true}, {"connections_allow_private", "not a range", false},
		{"proxy_auth_enabled", "true", true}, {"proxy_auth_enabled", "1", false},
		{"log_level", "debug", true}, {"log_level", "loud", false},
		{"smtp_password", "any secret you like", true}, {"smtp_password", "two\nlines", false},
		{"no_such_option", "x", false},
	} {
		if err := Validate(tc.key, tc.value); (err == nil) != tc.ok {
			t.Errorf("Validate(%s, %q) = %v, want ok=%v", tc.key, tc.value, err, tc.ok)
		}
	}
}

// Saved values reach the configuration at start-up, over what the environment gave.
func TestSavedValuesAreAppliedToTheConfigurationAtStartUp(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	sec := memSecrets{}
	cfg, err := config.LoadFrom(env(map[string]string{"SYNTAXIS_IDLE_MINUTES": "45", "SYNTAXIS_SMTP_HOST": "docker.example", "SYNTAXIS_EGRESS_MODE": "open"}))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"idle_minutes": "10", "sandbox_max_age_days": "3", "egress_mode": "allowlist", "egress_allowlist": "github.com, pypi.org",
		"smtp_port": "465", "base_url": "https://chat.example.com/", "proxy_auth_trusted": "10.0.0.1,10.0.0.2", "workspace_retention_days": "0", "signing_mode": "required"} {
		_ = st.SetSetting(ctx, k, v)
	}
	_ = sec.Put(ctx, SecretName("smtp_password"), "saved-secret")
	if w := ApplyToConfig(ctx, cfg, st, sec); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	if cfg.IdleTimeout != 10*time.Minute || cfg.SandboxMaxAge != 72*time.Hour || cfg.WorkspaceRetention != 0 {
		t.Errorf("durations: %v %v %v", cfg.IdleTimeout, cfg.SandboxMaxAge, cfg.WorkspaceRetention)
	}
	if cfg.EgressMode != "allowlist" || len(cfg.EgressAllow) != 2 || cfg.EgressAllow[1] != "pypi.org" {
		t.Errorf("egress: %s %v", cfg.EgressMode, cfg.EgressAllow)
	}
	if cfg.SMTP.Host != "docker.example" || cfg.SMTP.Port != 465 || cfg.SMTP.Password != "saved-secret" {
		t.Errorf("an option with nothing saved keeps the environment's value; saved ones replace it: %+v", cfg.SMTP)
	}
	if cfg.BaseURL != "https://chat.example.com" || len(cfg.ProxyAuth.Trusted) != 2 || cfg.SigningMode != "required" {
		t.Errorf("%s %v %s", cfg.BaseURL, cfg.ProxyAuth.Trusted, cfg.SigningMode)
	}
	// a saved value that is no longer acceptable is skipped with a warning, not applied and not fatal:
	// the instance still starts, and the admin can fix it in the app
	_ = st.SetSetting(ctx, "egress_mode", "nonsense")
	_ = st.SetSetting(ctx, "idle_minutes", "soon")
	good := *cfg
	w := ApplyToConfig(ctx, cfg, st, sec)
	if len(w) != 2 {
		t.Errorf("want two warnings, got %v", w)
	}
	if cfg.EgressMode != good.EgressMode || cfg.IdleTimeout != good.IdleTimeout {
		t.Errorf("an invalid saved value was applied: %s %v", cfg.EgressMode, cfg.IdleTimeout)
	}
}

func TestSMTPSettingsInEffect(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	sec := memSecrets{}
	e := env(map[string]string{"SYNTAXIS_SMTP_HOST": "docker.example", "SYNTAXIS_SMTP_USER": "u", "SYNTAXIS_SMTP_PASSWORD": "docker-pass"})
	host, port, user, pass, from := SMTPFrom(ctx, st, sec, e)
	if host != "docker.example" || port != 587 || user != "u" || pass != "docker-pass" || from != "syntaxis@localhost" {
		t.Fatalf("environment: %s %d %s %s %s", host, port, user, pass, from)
	}
	_ = st.SetSetting(ctx, "smtp_host", "saved.example")
	_ = st.SetSetting(ctx, "smtp_from", "me@example.com")
	_ = sec.Put(ctx, SecretName("smtp_password"), "saved-pass")
	host, _, _, pass, from = SMTPFrom(ctx, st, sec, e)
	if host != "saved.example" || pass != "saved-pass" || from != "me@example.com" {
		t.Fatalf("saved: %s %s %s", host, pass, from)
	}
}
