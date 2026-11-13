// SPDX-License-Identifier: GPL-2.0-or-later

// Command syntaxis is the Syntaxis server and admin CLI.
package main

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kolonuk/syntaxis/internal/auth"
	"github.com/kolonuk/syntaxis/internal/config"
	"github.com/kolonuk/syntaxis/internal/doctor"
	"github.com/kolonuk/syntaxis/internal/egress"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/quota"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/server"
	"github.com/kolonuk/syntaxis/internal/signing"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
	"github.com/kolonuk/syntaxis/internal/webui"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

const usage = `syntaxis ` + "%s" + `

Usage:
  syntaxis serve                       run the server
  syntaxis admin create <email> <name> create the first/another admin (password from SYNTAXIS_NEW_ADMIN_PASSWORD or stdin)
  syntaxis keygen [--vapid]            print a new master key (or VAPID key pair)
  syntaxis doctor                      check Docker, gVisor, providers, SMTP and configuration
  syntaxis backup <dest.db>            write a consistent database backup
  syntaxis rotate-key <old-key-file> <new-key-file>   re-encrypt all secrets under a new master key
  syntaxis egress-rules <subnet> [resolver-ip] [gateway-ip] [llm-cidr...]   print the nftables ruleset for the sandbox network (apply as root on the Docker host with nft -f -)
  syntaxis version
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errw io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintf(errw, usage, version)
		return errors.New("no command given")
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(out, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprintf(out, usage, version)
		return nil
	case "keygen":
		return cmdKeygen(args[1:], out)
	case "serve":
		return cmdServe()
	case "admin":
		return cmdAdmin(args[1:], out)
	case "doctor":
		return cmdDoctor(out)
	case "backup":
		return cmdBackup(args[1:], out)
	case "rotate-key":
		return cmdRotate(args[1:], out)
	case "egress-rules":
		return cmdEgressRules(args[1:], out)
	}
	fmt.Fprintf(errw, usage, version)
	return fmt.Errorf("unknown command %q", args[0])
}

func cmdKeygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	vapid := fs.Bool("vapid", false, "generate a VAPID key pair for Web Push")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *vapid {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "SYNTAXIS_VAPID_PUBLIC=%s\nSYNTAXIS_VAPID_PRIVATE=%s\n",
			base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(priv.Bytes()))
		return nil
	}
	k, err := vault.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, k)
	return nil
}

func openStore(cfg *config.Config) (*store.SQLite, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	return store.OpenSQLite(filepath.Join(cfg.DataDir, "syntaxis.db"))
}

func newLogger(cfg *config.Config) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.LogFormat == "text" || cfg.LogFormat == "pretty" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func mailer(cfg *config.Config, log *slog.Logger) mail.Mailer {
	if cfg.DevMailMode() {
		return mail.Console{Log: log}
	}
	return &mail.SMTP{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, User: cfg.SMTP.User, Password: cfg.SMTP.Password, From: cfg.SMTP.From}
}

func readPassword() (string, error) {
	if p := os.Getenv("SYNTAXIS_NEW_ADMIN_PASSWORD"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "Password (read from stdin, not echoed by this program): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func cmdAdmin(args []string, out io.Writer) error {
	if len(args) != 3 || args[0] != "create" {
		return errors.New("usage: syntaxis admin create <email> <name>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.MasterKey) != 32 {
		return errors.New("a master key is required (SYNTAXIS_MASTER_KEY or _FILE); generate one with `syntaxis keygen`")
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	v, _ := vault.New(cfg.MasterKey)
	as := auth.NewService(st, v, mail.Console{}, cfg.BaseURL)
	id, err := as.CreateAdmin(context.Background(), args[1], args[2], pw)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "created admin %d. Sign in on the admin address; 2FA setup is mandatory at first login.\n", id)
	return nil
}

func cmdBackup(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: syntaxis backup <dest.db>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(context.Background(), args[0]); err != nil {
		return err
	}
	_ = st.SetSetting(context.Background(), "last_backup_unix", strconv.FormatInt(time.Now().Unix(), 10))
	fmt.Fprintf(out, "database backed up to %s\nAlso back up workspace volumes (docker volume ls --filter label=io.syntaxis.managed) and keep the master key separately: without it secrets and 2FA cannot be decrypted.\n", args[0])
	return nil
}

func cmdRotate(args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: syntaxis rotate-key <old-key-file> <new-key-file>")
	}
	load := func(p string) (*vault.Vault, error) {
		b, err := os.ReadFile(p) //nolint:gosec // operator-provided
		if err != nil {
			return nil, err
		}
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, err
		}
		return vault.New(k)
	}
	oldV, err := load(args[0])
	if err != nil {
		return err
	}
	newV, err := load(args[1])
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	users, _ := st.ListUsers(ctx)
	var ids []int64
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	n, err := secrets.RotateAll(ctx, st, oldV, newV, ids)
	if err != nil {
		return fmt.Errorf("rotation stopped after %d secrets (restore from backup before retrying): %w", n, err)
	}
	fmt.Fprintf(out, "re-encrypted %d secrets. Provider keys, TOTP secrets and signing keys are rotated by re-saving them; restart with the new key.\n", n)
	return nil
}

func cmdEgressRules(args []string, out io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: syntaxis egress-rules <subnet> [resolver-ip] [gateway-ip] [llm-cidr...]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	subnet, err := netip.ParsePrefix(args[0])
	if err != nil {
		return fmt.Errorf("bad subnet: %w", err)
	}
	var resolver, gw netip.Addr
	if len(args) > 1 && args[1] != "-" {
		if resolver, err = netip.ParseAddr(args[1]); err != nil {
			return fmt.Errorf("bad resolver ip: %w", err)
		}
	}
	if len(args) > 2 && args[2] != "-" {
		if gw, err = netip.ParseAddr(args[2]); err != nil {
			return fmt.Errorf("bad gateway ip: %w", err)
		}
	}
	pol := egress.Policy{Mode: cfg.EgressMode, Allowlist: cfg.EgressAllow, BridgeGateway: gw}
	for _, c := range args[min(3, len(args)):] {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("bad CIDR %q: %w", c, err)
		}
		pol.ExtraBlocked = append(pol.ExtraBlocked, p)
	}
	rs, err := egress.Nftables(cfg.Docker.Network, subnet, resolver, pol)
	if err != nil {
		return err
	}
	fmt.Fprint(out, rs.Text)
	return nil
}

func cmdDoctor(out io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	fs := collectHealth(context.Background(), cfg, st, nil)
	if len(fs) == 0 {
		fmt.Fprintln(out, "All checks passed.")
		return nil
	}
	bad := false
	for _, f := range fs {
		fmt.Fprintf(out, "[%s] %s %s\n    %s\n    fix: %s\n", strings.ToUpper(string(f.Severity)), f.CheckID, f.Subject, f.Message, f.Fix)
		if f.Severity == health.Error {
			bad = true
		}
	}
	if bad {
		return errors.New("one or more errors found")
	}
	return nil
}

func newRuntime(cfg *config.Config) sandbox.Runtime {
	rt, err := sandbox.NewDocker(cfg.Docker.Host)
	if err != nil {
		return nil
	}
	return rt
}

func collectHealth(ctx context.Context, cfg *config.Config, st store.Store, rt sandbox.Runtime) []health.Finding {
	if rt == nil {
		rt = newRuntime(cfg)
	}
	// a nil interface holding a typed nil would panic; newRuntime returns a true nil on error
	state := doctor.Collect(ctx, doctor.Deps{Cfg: cfg, Store: st, Runtime: rt, NewProv: providerFactory(cfg, st)})
	return health.NewEngine(health.DefaultThresholds()).Run(state)
}

func providerFactory(cfg *config.Config, _ store.Store) func(store.Provider) (provider.Provider, error) {
	var v *vault.Vault
	if len(cfg.MasterKey) == 32 {
		v, _ = vault.New(cfg.MasterKey)
	}
	return func(p store.Provider) (provider.Provider, error) {
		key := ""
		if len(p.APIKeyEnc) > 0 && v != nil {
			pt, err := v.Open(p.APIKeyEnc, "provider:"+p.Name)
			if err != nil {
				return nil, err
			}
			key = string(pt)
		}
		return provider.New(provider.Config{Kind: p.Kind, BaseURL: p.BaseURL, APIKey: key})
	}
}

func cmdServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	if len(cfg.MasterKey) != 32 {
		return errors.New("a master key is required (SYNTAXIS_MASTER_KEY or SYNTAXIS_MASTER_KEY_FILE); generate one with `syntaxis keygen`")
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	v, err := vault.New(cfg.MasterKey)
	if err != nil {
		return err
	}
	ml := mailer(cfg, log)
	as := auth.NewService(st, v, ml, cfg.BaseURL)
	as.Rotation = func(ctx context.Context) int {
		s, _ := st.GetSetting(ctx, server.SettingRotationDays, strconv.Itoa(cfg.PasswordRotationDays))
		n, err := strconv.Atoi(s)
		if err != nil {
			return cfg.PasswordRotationDays
		}
		return n
	}
	q := quota.New(st, func() quota.Defaults {
		ctx := context.Background()
		dd, _ := st.GetSetting(ctx, server.SettingDailyTokens, "0")
		mm, _ := st.GetSetting(ctx, server.SettingMonthlyTokens, "0")
		d, _ := strconv.ParseInt(dd, 10, 64)
		m, _ := strconv.ParseInt(mm, 10, 64)
		return quota.Defaults{DailyTokens: d, MonthlyTokens: m, MaxSessions: 3, RequestsPerMinute: 60, MaxParallelRequests: 2, SandboxSecondsDaily: 8 * 3600, MaxSessionWallClock: 4 * time.Hour}
	})
	sec := &secrets.Service{Store: st, Vault: v}
	app := server.New(cfg, st, as, q, sec, ml, log)
	app.Signing = &signing.Service{Store: st, Vault: v}
	app.Web, app.AdminWeb = webui.Handler("user"), webui.Handler("admin")

	rt := newRuntime(cfg)
	if rt != nil {
		scfg := sandbox.DefaultConfig()
		scfg.Runtime, scfg.Image, scfg.Network = cfg.Docker.Runtime, cfg.Docker.Image, cfg.Docker.Network
		app.Sandbox = sandbox.NewManager(rt, scfg, log)
	} else {
		log.Warn("Docker client could not be created; sandboxes are unavailable")
	}
	app.RunAgent = app.RunAgentFunc(server.Wiring{})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bootstrapAdmin(ctx, cfg, as, st, log)

	go loop(ctx, 5*time.Minute, func() { app.SetHealth(collectHealth(ctx, cfg, st, rt)) })
	if app.Sandbox != nil {
		go loop(ctx, 10*time.Minute, func() { reap(ctx, app, st, cfg, log) })
		reap(ctx, app, st, cfg, log) // on boot: clean up orphans
	}
	go loop(ctx, time.Hour, func() { _, _ = st.PurgeExpiredAuthSessions(ctx, time.Now()) })

	userSrv := &http.Server{Addr: cfg.Listen, Handler: app.UserHandler(), ReadHeaderTimeout: 10 * time.Second}
	adminSrv := &http.Server{Addr: cfg.AdminListen, Handler: app.AdminHandler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	go func() { log.Info("chat app listening", "addr", cfg.Listen); errc <- userSrv.ListenAndServe() }()
	go func() { log.Info("admin app listening", "addr", cfg.AdminListen); errc <- adminSrv.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = userSrv.Shutdown(sctx)
	_ = adminSrv.Shutdown(sctx)
	return nil
}

func loop(ctx context.Context, every time.Duration, fn func()) {
	fn()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func reap(ctx context.Context, app *server.App, st store.Store, cfg *config.Config, log *slog.Logger) {
	res, err := app.Sandbox.Reap(ctx, sandbox.ReapPolicy{IdleAfter: cfg.IdleTimeout, DestroyAfter: cfg.SandboxMaxAge, Known: func(id string) bool {
		_, err := st.GetChatSession(ctx, id)
		return err == nil
	}})
	if err != nil {
		log.Warn("reaper failed", "err", err)
		return
	}
	if n := len(res.Orphans) + len(res.Stopped) + len(res.Destroyed); n > 0 {
		log.Info("reaper", "orphans", len(res.Orphans), "stopped", len(res.Stopped), "destroyed", len(res.Destroyed))
	}
}

func bootstrapAdmin(ctx context.Context, cfg *config.Config, as *auth.Service, st store.Store, log *slog.Logger) {
	if cfg.BootstrapAdminEmail == "" || cfg.BootstrapAdminPassword == "" {
		return
	}
	if n, _ := st.CountAccounts(ctx, store.KindAdmin); n > 0 {
		return
	}
	if _, err := as.CreateAdmin(ctx, cfg.BootstrapAdminEmail, "Administrator", cfg.BootstrapAdminPassword); err != nil {
		log.Error("bootstrap admin failed", "err", err)
		return
	}
	log.Info("bootstrap admin created; remove SYNTAXIS_BOOTSTRAP_ADMIN_PASSWORD from the environment")
}
