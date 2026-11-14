// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for command execution.
const (
	DefaultExecTimeout = 2 * time.Minute
	MaxExecTimeout     = 30 * time.Minute
	DefaultMaxOutput   = 1 << 20
)

// ErrNoRuntime means gVisor is not registered; Syntaxis refuses to fall back to runc.
var ErrNoRuntime = errors.New("sandbox: the gVisor runtime (runsc) is not registered with Docker; refusing to start an unisolated container")

// Manager owns sandbox lifecycle.
type Manager struct {
	RT  Runtime
	Cfg Config
	Log *slog.Logger
	Now func() time.Time

	mu          sync.Mutex
	activity    map[string]time.Time // session -> last activity
	killed      bool                 // global kill switch engaged
	killedUsers map[int64]bool
	dns         []string // resolvers handed to new sandboxes (see ensureDNS)
	dnsWritten  string   // resolv.conf content currently in the DNS volume
}

// NewManager returns a Manager.
func NewManager(rt Runtime, cfg Config, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{RT: rt, Cfg: cfg, Log: log, Now: time.Now, activity: map[string]time.Time{}, killedUsers: map[int64]bool{}}
}

// SetDNS sets the resolvers new sandboxes use.
func (m *Manager) SetDNS(dns []string) {
	m.mu.Lock()
	m.dns = append([]string(nil), dns...)
	m.mu.Unlock()
}

// Config returns the sandbox config with the current DNS servers filled in.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.Cfg
	if len(m.dns) > 0 {
		c.DNS = append([]string(nil), m.dns...)
	}
	return c
}

// ErrKilled is returned while the admin kill switch is engaged.
var ErrKilled = errors.New("sandbox: sandboxes are disabled by an administrator")

// Create provisions and starts the sandbox for a session. It returns the container id.
func (m *Manager) Create(ctx context.Context, userID int64, sessionID string, env map[string]string, networkDisabled bool) (string, error) {
	m.mu.Lock()
	killed := m.killed || m.killedUsers[userID]
	m.mu.Unlock()
	if killed {
		return "", ErrKilled
	}
	ok, err := m.RT.HasRuntime(ctx, m.Cfg.Runtime)
	if err != nil {
		return "", fmt.Errorf("check runtime: %w", err)
	}
	if !ok {
		return "", ErrNoRuntime
	}
	if img, err := m.RT.ImagePresent(ctx, m.Cfg.Image); err != nil {
		return "", err
	} else if !img {
		return "", fmt.Errorf("sandbox image %q is not present on the Docker host", m.Cfg.Image)
	}
	if err := m.RT.EnsureNetwork(ctx, m.Cfg.Network); err != nil {
		return "", fmt.Errorf("ensure network: %w", err)
	}
	if err := m.RT.EnsureVolume(ctx, WorkspaceVolume(userID), map[string]string{LabelManaged: "true", LabelUser: strconv.FormatInt(userID, 10)}); err != nil {
		return "", fmt.Errorf("ensure workspace: %w", err)
	}
	cfg := m.Config()
	if err := m.ensureDNS(ctx, cfg.DNS); err != nil {
		return "", fmt.Errorf("configure sandbox DNS: %w", err)
	}
	spec, err := BuildSpec(cfg, userID, sessionID, env, networkDisabled)
	if err != nil {
		return "", err
	}
	spec.Labels[LabelCreated] = strconv.FormatInt(m.Now().Unix(), 10)
	id, err := m.RT.Create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if err := m.RT.Start(ctx, id); err != nil {
		_ = m.RT.Remove(ctx, id)
		return "", fmt.Errorf("start container: %w", err)
	}
	m.touch(sessionID)
	m.Log.Info("sandbox created", "session", sessionID, "user", userID, "container", id)
	return id, nil
}

// Touch marks a sandbox as in use so the idle reaper leaves it alone.
func (m *Manager) Touch(sessionID string) { m.touch(sessionID) }

// OpenTerminal starts an interactive shell in a session's sandbox. It is the same
// container, user, limits and network policy as the agent's commands.
func (m *Manager) OpenTerminal(ctx context.Context, sessionID, containerID string, rows, cols uint, extraEnv []string) (*TTY, error) {
	m.mu.Lock()
	killed := m.killed
	m.mu.Unlock()
	if killed {
		return nil, ErrKilled
	}
	if rows == 0 || cols == 0 {
		rows, cols = 24, 80
	}
	m.touch(sessionID)
	return m.RT.ExecTTY(ctx, containerID, TTYSpec{
		Cmd:        []string{"sh", "-c", "exec bash -l 2>/dev/null || exec sh"},
		Env:        append([]string{"TERM=xterm-256color", "LANG=C.UTF-8"}, extraEnv...),
		WorkingDir: WorkspacePath,
		Rows:       rows, Cols: cols,
	})
}

func (m *Manager) touch(sessionID string) {
	m.mu.Lock()
	m.activity[sessionID] = m.Now()
	m.mu.Unlock()
}

// Exec runs a command in a session's sandbox with enforced timeout and output cap.
func (m *Manager) Exec(ctx context.Context, sessionID, containerID string, e ExecSpec) (ExecResult, error) {
	m.mu.Lock()
	killed := m.killed
	m.mu.Unlock()
	if killed {
		return ExecResult{}, ErrKilled
	}
	if e.Timeout <= 0 {
		e.Timeout = DefaultExecTimeout
	}
	if e.Timeout > MaxExecTimeout {
		e.Timeout = MaxExecTimeout
	}
	if e.MaxOutput <= 0 {
		e.MaxOutput = DefaultMaxOutput
	}
	if e.WorkingDir == "" {
		e.WorkingDir = WorkspacePath
	}
	m.touch(sessionID)
	cctx, cancel := context.WithTimeout(ctx, e.Timeout+5*time.Second)
	defer cancel()
	res, err := m.RT.Exec(cctx, containerID, e)
	m.touch(sessionID)
	if err != nil {
		return res, err
	}
	if res.TimedOut {
		return res, ErrTimeout
	}
	return res, nil
}

// Stop stops a container (workspace persists).
func (m *Manager) Stop(ctx context.Context, containerID string) error {
	return m.RT.Stop(ctx, containerID, 10*time.Second)
}

// Resume restarts or unpauses a container.
func (m *Manager) Resume(ctx context.Context, sessionID, containerID string) error {
	ci, err := m.RT.Inspect(ctx, containerID)
	if err != nil {
		return err
	}
	switch {
	case ci.Paused:
		err = m.RT.Unpause(ctx, containerID)
	case !ci.Running:
		err = m.RT.Start(ctx, containerID)
	}
	if err == nil {
		m.touch(sessionID)
	}
	return err
}

// Destroy removes a container (workspace volume is kept).
func (m *Manager) Destroy(ctx context.Context, sessionID, containerID string) error {
	_ = m.RT.Stop(ctx, containerID, 5*time.Second)
	err := m.RT.Remove(ctx, containerID)
	m.mu.Lock()
	delete(m.activity, sessionID)
	m.mu.Unlock()
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ReapPolicy controls the periodic reaper.
type ReapPolicy struct {
	IdleAfter    time.Duration // stop containers idle this long (0 = off)
	DestroyAfter time.Duration // remove containers unused this long (0 = off)
	// Known returns whether a session id still exists in the database.
	Known func(sessionID string) bool
}

// ReapResult summarises one pass.
type ReapResult struct {
	Orphans, Stopped, Destroyed []string
}

// Reap reconciles Docker against the database: orphans (no session) are
// removed, idle running containers are stopped, long-unused ones destroyed.
// Run on boot and periodically so stale gVisor containers never accumulate.
func (m *Manager) Reap(ctx context.Context, p ReapPolicy) (ReapResult, error) {
	var res ReapResult
	cs, err := m.RT.List(ctx)
	if err != nil {
		return res, err
	}
	now := m.Now()
	for _, c := range cs {
		sid := c.Labels[LabelSession]
		if p.Known != nil && !p.Known(sid) {
			if err := m.RT.Remove(ctx, c.ID); err == nil || errors.Is(err, ErrNotFound) {
				res.Orphans = append(res.Orphans, c.Name)
			}
			continue
		}
		m.mu.Lock()
		last, ok := m.activity[sid]
		m.mu.Unlock()
		if !ok {
			last = c.Created // after a restart we only know creation time
		}
		idle := now.Sub(last)
		if p.DestroyAfter > 0 && idle > p.DestroyAfter {
			if err := m.RT.Remove(ctx, c.ID); err == nil || errors.Is(err, ErrNotFound) {
				res.Destroyed = append(res.Destroyed, c.Name)
			}
			continue
		}
		if p.IdleAfter > 0 && c.Running && idle > p.IdleAfter {
			if err := m.RT.Stop(ctx, c.ID, 10*time.Second); err == nil {
				res.Stopped = append(res.Stopped, c.Name)
			}
		}
	}
	return res, nil
}

// KillAll engages the global kill switch and stops every managed container.
func (m *Manager) KillAll(ctx context.Context) (int, error) {
	m.mu.Lock()
	m.killed = true
	m.mu.Unlock()
	return m.killMatching(ctx, func(ContainerInfo) bool { return true })
}

// KillUser stops one user's containers and blocks new ones until Resume.
func (m *Manager) KillUser(ctx context.Context, userID int64) (int, error) {
	m.mu.Lock()
	m.killedUsers[userID] = true
	m.mu.Unlock()
	uid := strconv.FormatInt(userID, 10)
	return m.killMatching(ctx, func(c ContainerInfo) bool { return c.Labels[LabelUser] == uid })
}

// ReleaseKill lifts the global kill switch (users stay killed until ReleaseUser).
func (m *Manager) ReleaseKill() { m.mu.Lock(); m.killed = false; m.mu.Unlock() }

// ReleaseUser lifts a user's kill.
func (m *Manager) ReleaseUser(userID int64) {
	m.mu.Lock()
	delete(m.killedUsers, userID)
	m.mu.Unlock()
}

func (m *Manager) killMatching(ctx context.Context, match func(ContainerInfo) bool) (int, error) {
	cs, err := m.RT.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, c := range cs {
		if !match(c) {
			continue
		}
		if err := m.RT.Stop(ctx, c.ID, 2*time.Second); err != nil && !errors.Is(err, ErrNotFound) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// DNSVolume holds the one shared, read-only resolv.conf that sandboxes mount.
const DNSVolume = "syntaxis-dns"

// ResolvConf renders resolv.conf for the given servers.
func ResolvConf(dns []string) (string, error) {
	var b strings.Builder
	for _, d := range dns {
		if _, err := netip.ParseAddr(d); err != nil {
			return "", fmt.Errorf("invalid DNS server %q", d)
		}
		fmt.Fprintf(&b, "nameserver %s\n", d)
	}
	return b.String(), nil
}

// ensureDNS writes the shared resolv.conf into the DNS volume when it changed.
// Under gVisor, Docker's embedded resolver (127.0.0.11) on user-defined
// networks does not work, and /etc/resolv.conf is read-only in a read-only
// rootfs, so sandboxes mount this file (volume subpath) over /etc/resolv.conf.
func (m *Manager) ensureDNS(ctx context.Context, dns []string) error {
	if len(dns) == 0 {
		return nil
	}
	content, err := ResolvConf(dns)
	if err != nil {
		return err
	}
	m.mu.Lock()
	same := m.dnsWritten == content
	m.mu.Unlock()
	if same {
		return nil
	}
	if err := m.RT.WriteVolumeFile(ctx, DNSVolume, "resolv.conf", content, m.Cfg.Image); err != nil {
		return err
	}
	m.mu.Lock()
	m.dnsWritten = content
	m.mu.Unlock()
	return nil
}
