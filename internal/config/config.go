// SPDX-License-Identifier: GPL-2.0-or-later

// Package config loads Syntaxis configuration from the environment.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration. Every environment-specific value
// lives here; nothing is hardcoded elsewhere.
type Config struct {
	Listen       string
	AdminListen  string
	BaseURL      string
	DataDir      string
	MasterKey    []byte
	LogLevel     string
	LogFormat    string
	SMTP         SMTP
	Docker       Docker
	EgressMode   string
	EgressAllow  []string
	EgressBlock  []string // extra CIDRs sandboxes must never reach (e.g. the LLM server)
	DNSListen    string   // address for the sandbox DNS resolver (default: sandbox network gateway :53)
	DNSFallback  string   // resolver sandboxes use if the Syntaxis resolver cannot run (default 1.1.1.1)
	DNSUpstream  string   // upstream resolver host:port (default: first nameserver in /etc/resolv.conf)
	AdminReadMsg bool
	ProxyAuth    ProxyAuth
	VAPIDPublic  string
	VAPIDPrivate string

	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	// Policy defaults (admin can change at runtime; these are first-boot values).
	PasswordRotationDays int
	SecretScanMode       string // off | warn | block
	AllowUserOverride    bool
	SigningMode          string // off | optional | required
	IdleTimeout          time.Duration
	SandboxMaxAge        time.Duration
	WorkspaceRetention   time.Duration
}

// SMTP holds mail settings. An empty Host means dev mode (log emails).
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// Docker holds sandbox runtime settings.
type Docker struct {
	Host    string
	Runtime string
	Image   string
	Network string
}

// ProxyAuth configures optional trusted reverse-proxy header authentication.
type ProxyAuth struct {
	Enabled bool
	Header  string
	Trusted []string
}

// Getenv abstracts os.Getenv for testing.
type Getenv func(string) string

// Load reads configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration using get, validating values.
func LoadFrom(get Getenv) (*Config, error) {
	c := &Config{
		Listen:       str(get, "SYNTAXIS_LISTEN", "127.0.0.1:8080"),
		AdminListen:  str(get, "SYNTAXIS_ADMIN_LISTEN", "127.0.0.1:8081"),
		BaseURL:      strings.TrimRight(str(get, "SYNTAXIS_BASE_URL", "http://localhost:8080"), "/"),
		DataDir:      str(get, "SYNTAXIS_DATA_DIR", "./data"),
		LogLevel:     str(get, "SYNTAXIS_LOG_LEVEL", "info"),
		LogFormat:    str(get, "SYNTAXIS_LOG_FORMAT", "json"),
		EgressMode:   str(get, "SYNTAXIS_EGRESS_MODE", "open"),
		EgressAllow:  list(get("SYNTAXIS_EGRESS_ALLOWLIST")),
		EgressBlock:  list(get("SYNTAXIS_EGRESS_BLOCK")),
		DNSListen:    get("SYNTAXIS_DNS_LISTEN"),
		DNSUpstream:  get("SYNTAXIS_DNS_UPSTREAM"),
		DNSFallback:  str(get, "SYNTAXIS_DNS_FALLBACK", "1.1.1.1"),
		AdminReadMsg: boolean(get, "SYNTAXIS_ALLOW_ADMIN_READ_CONTENT", false),
		VAPIDPublic:  get("SYNTAXIS_VAPID_PUBLIC"),
		VAPIDPrivate: get("SYNTAXIS_VAPID_PRIVATE"),

		BootstrapAdminEmail:    get("SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: get("SYNTAXIS_BOOTSTRAP_ADMIN_PASSWORD"),

		SecretScanMode:    str(get, "SYNTAXIS_SECRET_SCAN", "warn"),
		AllowUserOverride: boolean(get, "SYNTAXIS_ALLOW_USER_OVERRIDE", true),
		SigningMode:       str(get, "SYNTAXIS_SIGNING_MODE", "off"),
	}
	c.SMTP = SMTP{
		Host:     get("SYNTAXIS_SMTP_HOST"),
		User:     get("SYNTAXIS_SMTP_USER"),
		Password: get("SYNTAXIS_SMTP_PASSWORD"),
		From:     str(get, "SYNTAXIS_SMTP_FROM", "syntaxis@localhost"),
	}
	var err error
	if c.SMTP.Port, err = integer(get, "SYNTAXIS_SMTP_PORT", 587); err != nil {
		return nil, err
	}
	c.Docker = Docker{
		Host:    get("SYNTAXIS_DOCKER_HOST"),
		Runtime: str(get, "SYNTAXIS_SANDBOX_RUNTIME", "runsc"),
		Image:   str(get, "SYNTAXIS_SANDBOX_IMAGE", "syntaxis-sandbox:latest"),
		Network: str(get, "SYNTAXIS_SANDBOX_NETWORK", "syntaxis-sandbox"),
	}
	c.ProxyAuth = ProxyAuth{
		Enabled: boolean(get, "SYNTAXIS_PROXY_AUTH_ENABLED", false),
		Header:  str(get, "SYNTAXIS_PROXY_AUTH_HEADER", "X-Forwarded-User"),
		Trusted: list(get("SYNTAXIS_PROXY_TRUSTED")),
	}
	if c.PasswordRotationDays, err = integer(get, "SYNTAXIS_PASSWORD_ROTATION_DAYS", 30); err != nil {
		return nil, err
	}
	idle, err := integer(get, "SYNTAXIS_IDLE_MINUTES", 30)
	if err != nil {
		return nil, err
	}
	c.IdleTimeout = time.Duration(idle) * time.Minute
	age, err := integer(get, "SYNTAXIS_SANDBOX_MAX_AGE_DAYS", 7)
	if err != nil {
		return nil, err
	}
	c.SandboxMaxAge = time.Duration(age) * 24 * time.Hour
	ret, err := integer(get, "SYNTAXIS_WORKSPACE_RETENTION_DAYS", 30)
	if err != nil {
		return nil, err
	}
	c.WorkspaceRetention = time.Duration(ret) * 24 * time.Hour

	if c.MasterKey, err = loadMasterKey(get); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

// Validate checks enumerated values.
func (c *Config) Validate() error {
	switch c.EgressMode {
	case "open", "allowlist", "none":
	default:
		return fmt.Errorf("invalid SYNTAXIS_EGRESS_MODE %q (want open|allowlist|none)", c.EgressMode)
	}
	switch c.SecretScanMode {
	case "off", "warn", "block":
	default:
		return fmt.Errorf("invalid SYNTAXIS_SECRET_SCAN %q (want off|warn|block)", c.SecretScanMode)
	}
	switch c.SigningMode {
	case "off", "optional", "required":
	default:
		return fmt.Errorf("invalid SYNTAXIS_SIGNING_MODE %q (want off|optional|required)", c.SigningMode)
	}
	if c.PasswordRotationDays < 0 {
		return errors.New("SYNTAXIS_PASSWORD_ROTATION_DAYS must be >= 0")
	}
	return nil
}

// DevMailMode reports whether emails are only logged.
func (c *Config) DevMailMode() bool { return c.SMTP.Host == "" }

func loadMasterKey(get Getenv) ([]byte, error) {
	raw := strings.TrimSpace(get("SYNTAXIS_MASTER_KEY"))
	if raw == "" {
		if path := get("SYNTAXIS_MASTER_KEY_FILE"); path != "" {
			b, err := os.ReadFile(path) //nolint:gosec // operator-provided path
			if err != nil {
				return nil, fmt.Errorf("read master key file: %w", err)
			}
			raw = strings.TrimSpace(string(b))
		}
	}
	if raw == "" {
		return nil, nil // allowed for commands that do not need the vault
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("master key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

func str(get Getenv, k, def string) string {
	if v := get(k); v != "" {
		return v
	}
	return def
}

func boolean(get Getenv, k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(get(k)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func integer(get Getenv, k string, def int) (int, error) {
	v := strings.TrimSpace(get(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
