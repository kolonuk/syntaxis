// SPDX-License-Identifier: GPL-2.0-or-later

// Package settings is the list of every option Syntaxis can be configured with, where each value
// comes from, and how a value saved in the admin app is applied.
//
// Most options have two possible homes. The Docker environment (the compose file or .env) gives
// the first value; the admin app can save a value in the database that replaces it. This package
// answers, for each option, which of the two is in effect right now, so the admin app can say so,
// and can put the environment's value back by removing the saved one. A few options belong to the
// deployment itself (listen addresses, the data directory, the Docker proxy and the like) and are
// shown but not edited here.
package settings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/safehttp"
)

// Where a value in effect comes from.
const (
	SourceDocker   = "docker"   // the environment (compose file or .env); nothing is saved in the app
	SourceDatabase = "database" // saved in the admin app; it replaces the environment's value
	SourceDefault  = "default"  // neither is set, so the built-in default applies
)

// Kind decides how the admin app draws an option and how a value is checked.
type Kind string

const (
	Text   Kind = "text"
	Number Kind = "number"
	Toggle Kind = "toggle"
	Choice Kind = "choice"
	Secret Kind = "secret" // write-only: shown as "set" or "not set", never as a value
	List   Kind = "list"   // comma-separated
)

// Groups, in the order the admin app shows them.
var Groups = []struct{ ID, Label, Help string }{
	{"email", "Email", "Where invite and password-reset emails are sent from. Without a server, emails are only written to the log."},
	{"addresses", "Addresses", "How people reach the apps. The public address is used in email links and decides whether cookies are Secure."},
	{"sandboxes", "Sessions and sandboxes", "How long sandboxes and workspaces live and how much compute each user gets per day."},
	{"security", "Security", "Defaults the policies below build on. The most specific setting for a user, group or level still wins."},
	{"proxy", "Reverse-proxy sign-in", "Trusting a header from a proxy in front of Syntaxis. Leave this off unless you run one you control."},
	{"logging", "Logging", "What the server writes to its log."},
	{"deployment", "Set by your deployment", "These belong to how Syntaxis is installed. Change them where it is deployed (the compose file or .env) and restart."},
}

// Def describes one option.
type Def struct {
	Key         string // where a saved value lives in the database
	Env         string // the environment variable that gives the first value
	Group       string
	Label       string
	Help        string
	Placeholder string
	Kind        Kind
	Options     []string
	Default     string
	// Live: a saved value takes effect at once. Otherwise it takes effect the next time Syntaxis starts.
	Live bool
	// Locked: shown for information only; it belongs to the deployment and is not changed here.
	Locked bool
	// Hidden hides a locked option's value (a key or token): only whether it is set is shown.
	Hidden bool
	// Validate checks a value before it is saved.
	Validate func(string) error
	// Apply puts an effective value into the configuration at start-up (nil: read where it is used).
	Apply func(*config.Config, string) error
}

func atoi(v string) (int, error) { return strconv.Atoi(strings.TrimSpace(v)) }

func nonNegative(v string) error {
	n, err := atoi(v)
	if err != nil || n < 0 {
		return errors.New("a whole number, 0 or more")
	}
	return nil
}

func oneOf(opts ...string) func(string) error {
	return func(v string) error {
		for _, o := range opts {
			if v == o {
				return nil
			}
		}
		return fmt.Errorf("one of %s", strings.Join(opts, ", "))
	}
}

func hostOrAddr(v string) error {
	if v == "" {
		return nil
	}
	if strings.ContainsAny(v, " \t\r\n/@") {
		return errors.New("a host name or address with no spaces, slashes or @")
	}
	return nil
}

func absoluteURL(v string) error {
	if v == "" {
		return nil
	}
	if !(strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")) || strings.ContainsAny(v, " \r\n") {
		return errors.New("an address starting with http:// or https://")
	}
	return nil
}

func noLineBreaks(v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return errors.New("one line")
	}
	return nil
}

func cidrOrHostList(v string) error {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, " \t\r\n") {
			return fmt.Errorf("%q has a space; separate entries with commas", p)
		}
	}
	return nil
}

func cidrList(v string) error {
	var parts []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if _, err := safehttp.ParseAllow(parts); err != nil {
		return err
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Defs is every option. Keys that already existed before this package keep their database key, so
// a value an admin saved earlier is still the one in effect.
var Defs = []Def{
	// ---- email ----
	{Key: "smtp_host", Env: "SYNTAXIS_SMTP_HOST", Group: "email", Label: "Mail server", Help: "The SMTP server that sends the emails. Leave empty to only log them.", Placeholder: "smtp.example.com", Kind: Text, Live: true, Validate: hostOrAddr,
		Apply: func(c *config.Config, v string) error { c.SMTP.Host = v; return nil }},
	{Key: "smtp_port", Env: "SYNTAXIS_SMTP_PORT", Group: "email", Label: "Port", Help: "587 (STARTTLS) is the usual port. Port 465, which starts with TLS, is not supported.", Kind: Number, Default: "587", Live: true,
		Validate: func(v string) error {
			if n, err := atoi(v); err != nil || n < 1 || n > 65535 {
				return errors.New("a port between 1 and 65535")
			}
			return nil
		},
		Apply: func(c *config.Config, v string) error { n, _ := atoi(v); c.SMTP.Port = n; return nil }},
	{Key: "smtp_user", Env: "SYNTAXIS_SMTP_USER", Group: "email", Label: "User name", Help: "Leave empty if the server needs no sign-in.", Kind: Text, Live: true, Validate: noLineBreaks,
		Apply: func(c *config.Config, v string) error { c.SMTP.User = v; return nil }},
	{Key: "smtp_password", Env: "SYNTAXIS_SMTP_PASSWORD", Group: "email", Label: "Password", Help: "Kept encrypted. It is never shown again; leave the box empty to keep the current one.", Kind: Secret, Live: true},
	{Key: "smtp_from", Env: "SYNTAXIS_SMTP_FROM", Group: "email", Label: "From address", Help: "The sender shown on emails.", Placeholder: "syntaxis@example.com", Kind: Text, Default: "syntaxis@localhost", Live: true, Validate: noLineBreaks,
		Apply: func(c *config.Config, v string) error { c.SMTP.From = v; return nil }},

	// ---- addresses ----
	{Key: "base_url", Env: "SYNTAXIS_BASE_URL", Group: "addresses", Label: "Public address of the chat app", Help: "What people type to open it. Used in email links; https:// turns on Secure cookies.", Placeholder: "https://syntaxis.example.com", Kind: Text, Default: "http://localhost:8080", Validate: absoluteURL,
		Apply: func(c *config.Config, v string) error { c.BaseURL = strings.TrimRight(v, "/"); return nil }},
	{Key: "admin_url", Env: "SYNTAXIS_ADMIN_URL", Group: "addresses", Label: "Address of this admin app", Help: "Adds an \"Admin mode\" link in the chat app for admin-level users. It does not skip the admin sign-in.", Placeholder: "https://syntaxis-admin.example.com", Kind: Text, Validate: absoluteURL,
		Apply: func(c *config.Config, v string) error { c.AdminURL = v; return nil }},

	// ---- sessions and sandboxes ----
	{Key: "idle_minutes", Env: "SYNTAXIS_IDLE_MINUTES", Group: "sandboxes", Label: "Stop idle sandboxes after (minutes)", Kind: Number, Default: "30", Validate: nonNegative,
		Apply: func(c *config.Config, v string) error {
			n, _ := atoi(v)
			c.IdleTimeout = time.Duration(n) * time.Minute
			return nil
		}},
	{Key: "sandbox_max_age_days", Env: "SYNTAXIS_SANDBOX_MAX_AGE_DAYS", Group: "sandboxes", Label: "Remove unused sandboxes after (days)", Kind: Number, Default: "7", Validate: nonNegative,
		Apply: func(c *config.Config, v string) error {
			n, _ := atoi(v)
			c.SandboxMaxAge = time.Duration(n) * 24 * time.Hour
			return nil
		}},
	{Key: "workspace_retention_days", Env: "SYNTAXIS_WORKSPACE_RETENTION_DAYS", Group: "sandboxes", Label: "Delete an unused workspace after (days)", Help: "0 keeps workspaces for ever.", Kind: Number, Default: "30", Validate: nonNegative,
		Apply: func(c *config.Config, v string) error {
			n, _ := atoi(v)
			c.WorkspaceRetention = time.Duration(n) * 24 * time.Hour
			return nil
		}},
	{Key: "cpu_minutes_daily", Env: "SYNTAXIS_CPU_MINUTES_DAILY", Group: "sandboxes", Label: "Compute each user may use per day (core-minutes)", Help: "0 is unlimited. A level, group or user can have its own.", Kind: Number, Default: "240", Validate: nonNegative,
		Apply: func(c *config.Config, v string) error { n, _ := atoi(v); c.CPUMinutesDaily = n; return nil }},
	{Key: "egress_mode", Env: "SYNTAXIS_EGRESS_MODE", Group: "sandboxes", Label: "What sandboxes can reach on the internet", Help: "open: everything except your network and this host. allowlist: only the hosts below. none: nothing. A user, group or level can override it.", Kind: Choice, Options: []string{"open", "allowlist", "none"}, Default: "open", Validate: oneOf("open", "allowlist", "none"),
		Apply: func(c *config.Config, v string) error { c.EgressMode = v; return nil }},
	{Key: "egress_allowlist", Env: "SYNTAXIS_EGRESS_ALLOWLIST", Group: "sandboxes", Label: "Allowed hosts (allowlist mode)", Help: "Comma-separated host names or address ranges.", Placeholder: "github.com, pypi.org, 203.0.113.0/24", Kind: List, Validate: cidrOrHostList,
		Apply: func(c *config.Config, v string) error { c.EgressAllow = splitList(v); return nil }},
	{Key: "connections_allow_private", Env: "SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE", Group: "sandboxes", Label: "Private networks the server may call for users' connections", Help: "A user's self-hosted GitLab or Gitea on your LAN. Only public addresses are reached unless listed here.", Placeholder: "192.0.2.0/24", Kind: List, Validate: cidrList,
		Apply: func(c *config.Config, v string) error { c.ConnectionsAllowPrivate = splitList(v); return nil }},

	// ---- security (these three were already saved in the database; the keys are unchanged) ----
	{Key: "password_rotation_days", Env: "SYNTAXIS_PASSWORD_ROTATION_DAYS", Group: "security", Label: "Password change every (days, users without 2FA)", Help: "0 switches rotation off.", Kind: Number, Default: "30", Live: true, Validate: nonNegative},
	{Key: "secret_scan_mode", Env: "SYNTAXIS_SECRET_SCAN", Group: "security", Label: "Secret scanner before pushes", Help: "off, warn (ask the user) or block.", Kind: Choice, Options: []string{"off", "warn", "block"}, Default: "warn", Live: true, Validate: oneOf("off", "warn", "block")},
	{Key: "secret_scan_allow_user_override", Env: "SYNTAXIS_ALLOW_USER_OVERRIDE", Group: "security", Label: "Users may push despite scanner findings", Help: "Every override is audited.", Kind: Toggle, Default: "true", Live: true, Validate: oneOf("true", "false")},
	{Key: "signing_mode", Env: "SYNTAXIS_SIGNING_MODE", Group: "security", Label: "SSH commit signing", Help: "off, optional or required.", Kind: Choice, Options: []string{"off", "optional", "required"}, Default: "off", Validate: oneOf("off", "optional", "required"),
		Apply: func(c *config.Config, v string) error { c.SigningMode = v; return nil }},

	// ---- reverse proxy ----
	{Key: "proxy_auth_enabled", Env: "SYNTAXIS_PROXY_AUTH_ENABLED", Group: "proxy", Label: "Trust sign-in headers from a reverse proxy", Help: "Only switch this on behind a proxy that sets the header itself and that nothing else can reach.", Kind: Toggle, Default: "false", Validate: oneOf("true", "false"),
		Apply: func(c *config.Config, v string) error { c.ProxyAuth.Enabled = v == "true"; return nil }},
	{Key: "proxy_auth_header", Env: "SYNTAXIS_PROXY_AUTH_HEADER", Group: "proxy", Label: "Header carrying the user", Kind: Text, Default: "X-Forwarded-User", Validate: noLineBreaks,
		Apply: func(c *config.Config, v string) error { c.ProxyAuth.Header = v; return nil }},
	{Key: "proxy_auth_trusted", Env: "SYNTAXIS_PROXY_TRUSTED", Group: "proxy", Label: "Proxy addresses whose headers are trusted", Help: "The only addresses or ranges whose header is believed. Never 0.0.0.0/0.", Placeholder: "172.31.250.2", Kind: List, Validate: cidrOrHostList,
		Apply: func(c *config.Config, v string) error { c.ProxyAuth.Trusted = splitList(v); return nil }},

	// ---- logging ----
	{Key: "log_level", Env: "SYNTAXIS_LOG_LEVEL", Group: "logging", Label: "Log level", Kind: Choice, Options: []string{"debug", "info", "warn", "error"}, Default: "info", Validate: oneOf("debug", "info", "warn", "error"),
		Apply: func(c *config.Config, v string) error { c.LogLevel = v; return nil }},
	{Key: "log_format", Env: "SYNTAXIS_LOG_FORMAT", Group: "logging", Label: "Log format", Help: "json for machines, text for reading.", Kind: Choice, Options: []string{"json", "text"}, Default: "json", Validate: oneOf("json", "text"),
		Apply: func(c *config.Config, v string) error { c.LogFormat = v; return nil }},

	// ---- belongs to the deployment: shown, not edited ----
	{Key: "listen", Env: "SYNTAXIS_LISTEN", Group: "deployment", Label: "Chat app listen address", Help: "Inside the container this is all interfaces; the published port decides who can connect.", Kind: Text, Default: "127.0.0.1:8080", Locked: true},
	{Key: "admin_listen", Env: "SYNTAXIS_ADMIN_LISTEN", Group: "deployment", Label: "Admin app listen address", Help: "Keep it off the public internet. Binding it publicly is an error in Health.", Kind: Text, Default: "127.0.0.1:8081", Locked: true},
	{Key: "data_dir", Env: "SYNTAXIS_DATA_DIR", Group: "deployment", Label: "Data directory", Kind: Text, Default: "./data", Locked: true},
	{Key: "docker_host", Env: "SYNTAXIS_DOCKER_HOST", Group: "deployment", Label: "Docker proxy", Help: "Syntaxis never mounts the Docker socket; it talks to the validating proxy.", Kind: Text, Locked: true},
	{Key: "sandbox_runtime", Env: "SYNTAXIS_SANDBOX_RUNTIME", Group: "deployment", Label: "Sandbox runtime", Kind: Text, Default: "runsc", Locked: true},
	{Key: "sandbox_image", Env: "SYNTAXIS_SANDBOX_IMAGE", Group: "deployment", Label: "Sandbox image", Kind: Text, Default: "syntaxis-sandbox:latest", Locked: true},
	{Key: "sandbox_network", Env: "SYNTAXIS_SANDBOX_NETWORK", Group: "deployment", Label: "Sandbox network", Kind: Text, Default: "syntaxis-sandbox", Locked: true},
	{Key: "dns_listen", Env: "SYNTAXIS_DNS_LISTEN", Group: "deployment", Label: "Sandbox DNS resolver address", Kind: Text, Locked: true},
	{Key: "dns_upstream", Env: "SYNTAXIS_DNS_UPSTREAM", Group: "deployment", Label: "DNS upstream", Kind: Text, Locked: true},
	{Key: "dns_fallback", Env: "SYNTAXIS_DNS_FALLBACK", Group: "deployment", Label: "DNS fallback", Kind: Text, Default: "1.1.1.1", Locked: true},
	{Key: "egress_block", Env: "SYNTAXIS_EGRESS_BLOCK", Group: "deployment", Label: "Extra ranges sandboxes can never reach", Help: "For example your model server. The host firewall reads this from .env.", Kind: Text, Locked: true},
	{Key: "master_key", Env: "SYNTAXIS_MASTER_KEY", Group: "deployment", Label: "Master key", Help: "Encrypts every stored secret. Never shown.", Kind: Secret, Locked: true, Hidden: true},
	{Key: "firewall_token", Env: "SYNTAXIS_FIREWALL_TOKEN", Group: "deployment", Label: "Host service token", Help: "Lets the host's firewall and update services talk to this app. Never shown.", Kind: Secret, Locked: true, Hidden: true},
}

// ByKey finds an option.
func ByKey(key string) (Def, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Store is the part of the database this package needs.
type Store interface {
	GetSetting(ctx context.Context, key, def string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
}

// SecretStore is where write-only values are kept, encrypted.
type SecretStore interface {
	Has(ctx context.Context, name string) bool
	Put(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
	Get(ctx context.Context, name string) (string, bool)
}

// SecretName is the name a secret option is stored under. These names are reserved: the admin app
// does not list them with the credentials servers can use, and refuses to map them.
func SecretName(key string) string { return "SYNTAXIS_" + strings.ToUpper(key) }

// ReservedSecret reports whether a stored secret's name belongs to Syntaxis itself.
func ReservedSecret(name string) bool { return strings.HasPrefix(name, "SYNTAXIS_") }

const absent = "\x00absent"

// Item is one option as the admin app shows it.
type Item struct {
	Key         string   `json:"key"`
	Group       string   `json:"group"`
	Label       string   `json:"label"`
	Help        string   `json:"help,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Kind        Kind     `json:"kind"`
	Options     []string `json:"options,omitempty"`
	Env         string   `json:"env,omitempty"`
	Default     string   `json:"default,omitempty"`
	Live        bool     `json:"live"`
	Locked      bool     `json:"locked"`
	// Value is what is in effect (empty for a secret). Source says where it comes from.
	Value  string `json:"value"`
	Source string `json:"source"`
	// EnvValue is the environment's value, so a saved one can be compared with it.
	EnvValue string `json:"env_value,omitempty"`
	EnvSet   bool   `json:"env_set"`
	// HasValue is whether a secret (or a hidden value) is set at all.
	HasValue bool `json:"has_value"`
}

// Resolve works out every option's value in effect and where it comes from. A value saved in the
// database replaces the environment's; the environment replaces the default.
func Resolve(ctx context.Context, db Store, secrets SecretStore, getenv func(string) string) []Item {
	out := make([]Item, 0, len(Defs))
	for _, d := range Defs {
		it := Item{Key: d.Key, Group: d.Group, Label: d.Label, Help: d.Help, Placeholder: d.Placeholder, Kind: d.Kind, Options: d.Options,
			Env: d.Env, Default: d.Default, Live: d.Live, Locked: d.Locked}
		envv := ""
		if d.Env != "" && getenv != nil {
			envv = strings.TrimSpace(getenv(d.Env))
		}
		it.EnvSet = envv != ""
		if d.Kind == Secret {
			saved := !d.Locked && secrets != nil && secrets.Has(ctx, SecretName(d.Key))
			switch {
			case saved:
				it.Source, it.HasValue = SourceDatabase, true
			case it.EnvSet:
				it.Source, it.HasValue = SourceDocker, true
			default:
				it.Source = SourceDefault
			}
			if d.Locked && it.EnvSet {
				it.Source = SourceDocker
			}
			out = append(out, it)
			continue
		}
		if !d.Locked {
			if v, err := db.GetSetting(ctx, d.Key, absent); err == nil && v != absent {
				it.Value, it.Source = v, SourceDatabase
				it.EnvValue = envv
				it.HasValue = true
				out = append(out, it)
				continue
			}
		}
		switch {
		case it.EnvSet:
			it.Value, it.Source, it.HasValue = envv, SourceDocker, true
		default:
			it.Value, it.Source = d.Default, SourceDefault
			it.HasValue = d.Default != ""
		}
		it.EnvValue = envv
		if d.Hidden {
			it.Value, it.EnvValue = "", ""
		}
		out = append(out, it)
	}
	return out
}

// Effective returns the value in effect for one option ("" when it has none).
func Effective(ctx context.Context, db Store, getenv func(string) string, key string) string {
	d, ok := ByKey(key)
	if !ok || d.Kind == Secret {
		return ""
	}
	if !d.Locked {
		if v, err := db.GetSetting(ctx, key, absent); err == nil && v != absent {
			return v
		}
	}
	if d.Env != "" && getenv != nil {
		if v := strings.TrimSpace(getenv(d.Env)); v != "" {
			return v
		}
	}
	return d.Default
}

// Validate checks a value for saving. Locked options cannot be saved.
func Validate(key, value string) error {
	d, ok := ByKey(key)
	if !ok {
		return fmt.Errorf("no such setting %q", key)
	}
	if d.Locked {
		return fmt.Errorf("%s belongs to the deployment; change it where Syntaxis is deployed", d.Label)
	}
	if d.Kind == Secret {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("a password cannot contain a line break")
		}
		return nil
	}
	if len(value) > 2000 {
		return errors.New("too long")
	}
	if d.Kind == Toggle && value != "true" && value != "false" {
		return errors.New("true or false")
	}
	if d.Validate != nil {
		if err := d.Validate(value); err != nil {
			return fmt.Errorf("%s: %w", d.Label, err)
		}
	}
	return nil
}

// ApplyToConfig puts the database's values over the configuration read from the environment, for
// options that are read once at start-up. Call it after the database is open and before anything
// is built from the configuration. Live options are applied too, so the start-up view matches.
//
// A saved value that is no longer acceptable is skipped, and reported in the returned warnings,
// instead of stopping Syntaxis from starting: the admin can fix it in the app.
func ApplyToConfig(ctx context.Context, cfg *config.Config, db Store, secrets SecretStore) (warnings []error) {
	for _, d := range Defs {
		if d.Locked {
			continue
		}
		if d.Kind == Secret {
			if d.Key == "smtp_password" && secrets != nil {
				if v, ok := secrets.Get(ctx, SecretName(d.Key)); ok {
					cfg.SMTP.Password = v
				}
			}
			continue
		}
		if d.Apply == nil {
			continue
		}
		v, err := db.GetSetting(ctx, d.Key, absent)
		if err != nil || v == absent {
			continue
		}
		if err := Validate(d.Key, v); err != nil {
			warnings = append(warnings, fmt.Errorf("the saved %s is ignored: %w", d.Key, err))
			continue
		}
		before := *cfg
		if err := d.Apply(cfg, v); err != nil {
			*cfg = before
			warnings = append(warnings, fmt.Errorf("the saved %s is ignored: %w", d.Key, err))
			continue
		}
		if err := cfg.Validate(); err != nil {
			*cfg = before
			warnings = append(warnings, fmt.Errorf("the saved %s is ignored: %w", d.Key, err))
		}
	}
	return warnings
}

// SMTPFrom reads the mail settings in effect: the saved ones where there are any, then the
// environment. Used for every send, so a change in the admin app applies at once.
func SMTPFrom(ctx context.Context, db Store, secrets SecretStore, getenv func(string) string) (host string, port int, user, password, from string) {
	host = Effective(ctx, db, getenv, "smtp_host")
	port, _ = atoi(Effective(ctx, db, getenv, "smtp_port"))
	user = Effective(ctx, db, getenv, "smtp_user")
	from = Effective(ctx, db, getenv, "smtp_from")
	password = strings.TrimSpace(getenvSafe(getenv, "SYNTAXIS_SMTP_PASSWORD"))
	if secrets != nil {
		if v, ok := secrets.Get(ctx, SecretName("smtp_password")); ok {
			password = v
		}
	}
	return
}

func getenvSafe(g func(string) string, k string) string {
	if g == nil {
		return ""
	}
	return g(k)
}
