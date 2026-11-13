// SPDX-License-Identifier: GPL-2.0-or-later

// Package sandbox creates and manages one gVisor-isolated container per
// session. The container spec is built by a pure function so every isolation
// property is unit-testable without Docker.
package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Labels applied to every Syntaxis container so the reaper can find orphans.
const (
	LabelManaged = "io.syntaxis.managed"
	LabelUser    = "io.syntaxis.user"
	LabelSession = "io.syntaxis.session"
	LabelCreated = "io.syntaxis.created"
)

// WorkspacePath is where the user's workspace volume is mounted.
const WorkspacePath = "/workspace"

// Limits are per-sandbox resource caps.
type Limits struct {
	MemoryBytes int64
	CPUs        float64
	PidsLimit   int64
	DiskBytes   int64 // enforced via volume size option where supported + watchdog
	TmpfsBytes  int64
	ShmBytes    int64
}

// DefaultLimits returns conservative defaults (operators override via config).
func DefaultLimits() Limits {
	return Limits{MemoryBytes: 2 << 30, CPUs: 2, PidsLimit: 512, DiskBytes: 5 << 30, TmpfsBytes: 256 << 20, ShmBytes: 64 << 20}
}

// Config configures the sandbox manager.
type Config struct {
	Runtime string // "runsc"
	Image   string
	Network string
	User    string // non-root uid:gid inside the container
	Limits  Limits
	DNS     []string // Syntaxis resolver address(es); sandboxes never get other DNS
}

// DefaultConfig fills defaults.
func DefaultConfig() Config {
	return Config{Runtime: "runsc", Image: "syntaxis-sandbox:latest", Network: "syntaxis-sandbox", User: "10001:10001", Limits: DefaultLimits()}
}

// Spec is the runtime-agnostic container specification.
type Spec struct {
	Name            string
	Image           string
	Runtime         string
	User            string
	Cmd             []string
	Env             []string
	Labels          map[string]string
	Network         string
	DNS             []string
	Mounts          []Mount
	Tmpfs           map[string]string // path -> options
	ReadOnlyRootFS  bool
	CapDrop         []string
	CapAdd          []string
	SecurityOpt     []string
	Privileged      bool
	PidsLimit       int64
	MemoryBytes     int64
	MemorySwap      int64
	NanoCPUs        int64
	ShmBytes        int64
	NetworkDisabled bool
	Init            bool
}

// Mount is a named-volume mount. Host bind mounts are not representable on
// purpose: a sandbox can only ever mount volumes.
type Mount struct {
	Volume   string
	Target   string
	ReadOnly bool
}

var volRe = regexp.MustCompile(`^syntaxis-ws-u[0-9]+$`)

// WorkspaceVolume is the per-user workspace volume name.
func WorkspaceVolume(userID int64) string { return fmt.Sprintf("syntaxis-ws-u%d", userID) }

// ContainerName is the per-session container name.
func ContainerName(sessionID string) string { return "syntaxis-s-" + sanitize(sessionID) }

var sanRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func sanitize(s string) string { return sanRe.ReplaceAllString(s, "_") }

// ErrBadSpec is returned when a spec violates isolation invariants.
var ErrBadSpec = errors.New("sandbox: spec violates isolation policy")

// BuildSpec builds the hardened spec for a session. env must already be
// redaction-safe (it carries only short-lived credentials the user chose to
// inject); the LLM server address and provider keys are never included.
func BuildSpec(c Config, userID int64, sessionID string, env map[string]string, networkDisabled bool) (Spec, error) {
	if c.Runtime == "" {
		return Spec{}, fmt.Errorf("%w: no runtime configured (gVisor runsc is required)", ErrBadSpec)
	}
	s := Spec{
		Name:    ContainerName(sessionID),
		Image:   c.Image,
		Runtime: c.Runtime,
		User:    c.User,
		Cmd:     []string{"sleep", "infinity"},
		Labels: map[string]string{
			LabelManaged: "true",
			LabelUser:    fmt.Sprint(userID),
			LabelSession: sessionID,
		},
		Network: c.Network,
		DNS:     c.DNS,
		Mounts:  []Mount{{Volume: WorkspaceVolume(userID), Target: WorkspacePath}},
		Tmpfs: map[string]string{
			"/tmp":      fmt.Sprintf("rw,exec,nosuid,size=%d", c.Limits.TmpfsBytes), // exec allowed: compilers and test runners run build output from here
			"/run":      "rw,noexec,nosuid,size=16777216",
			"/home/dev": fmt.Sprintf("rw,exec,nosuid,size=%d,uid=10001,gid=10001", c.Limits.TmpfsBytes),
		},
		ReadOnlyRootFS:  true,
		CapDrop:         []string{"ALL"},
		SecurityOpt:     []string{"no-new-privileges:true"},
		Privileged:      false,
		PidsLimit:       c.Limits.PidsLimit,
		MemoryBytes:     c.Limits.MemoryBytes,
		MemorySwap:      c.Limits.MemoryBytes, // no swap
		NanoCPUs:        int64(c.Limits.CPUs * 1e9),
		ShmBytes:        c.Limits.ShmBytes,
		NetworkDisabled: networkDisabled,
		Init:            true,
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.Env = append(s.Env, k+"="+env[k])
	}
	s.Env = append(s.Env, "HOME=/home/dev", "WORKSPACE="+WorkspacePath)
	return s, Validate(s, userID)
}

// Validate enforces isolation invariants on any spec before it reaches Docker.
func Validate(s Spec, ownerID int64) error {
	var errs []string
	if s.Runtime != "runsc" && !strings.HasPrefix(s.Runtime, "runsc") {
		errs = append(errs, "runtime must be gVisor (runsc)")
	}
	if s.Privileged {
		errs = append(errs, "privileged is forbidden")
	}
	if !s.ReadOnlyRootFS {
		errs = append(errs, "root filesystem must be read-only")
	}
	if len(s.CapDrop) != 1 || s.CapDrop[0] != "ALL" || len(s.CapAdd) > 0 {
		errs = append(errs, "capabilities must be dropped (cap_drop ALL, no cap_add)")
	}
	hasNNP := false
	for _, o := range s.SecurityOpt {
		if o == "no-new-privileges:true" || o == "no-new-privileges" {
			hasNNP = true
		}
		if strings.HasPrefix(o, "seccomp=unconfined") || strings.HasPrefix(o, "apparmor=unconfined") {
			errs = append(errs, "unconfined security profiles are forbidden")
		}
	}
	if !hasNNP {
		errs = append(errs, "no-new-privileges is required")
	}
	if s.User == "" || s.User == "root" || s.User == "0" || strings.HasPrefix(s.User, "0:") || strings.HasPrefix(s.User, "root:") {
		errs = append(errs, "must run as a non-root user")
	}
	if s.PidsLimit <= 0 || s.MemoryBytes <= 0 || s.NanoCPUs <= 0 {
		errs = append(errs, "pids, memory and CPU limits are required")
	}
	own := WorkspaceVolume(ownerID)
	for _, m := range s.Mounts {
		if !volRe.MatchString(m.Volume) {
			errs = append(errs, fmt.Sprintf("mount %q is not a workspace volume (host bind mounts and docker sockets are forbidden)", m.Volume))
		} else if m.Volume != own {
			errs = append(errs, fmt.Sprintf("volume %q does not belong to the session owner", m.Volume))
		}
		if strings.Contains(m.Target, "docker.sock") {
			errs = append(errs, "docker socket mounts are forbidden")
		}
	}
	for _, e := range s.Env {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(strings.ToUpper(k), "DOCKER_HOST") {
			errs = append(errs, "DOCKER_HOST must not be set in a sandbox")
		}
	}
	if s.Labels[LabelManaged] != "true" || s.Labels[LabelUser] != fmt.Sprint(ownerID) {
		errs = append(errs, "ownership labels missing or wrong")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrBadSpec, strings.Join(errs, "; "))
	}
	return nil
}
