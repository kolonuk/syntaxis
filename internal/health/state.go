// SPDX-License-Identifier: GPL-2.0-or-later

package health

import (
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
)

// State is a snapshot of everything the checks look at. Collectors fill it
// from live systems; tests fill it with fake data.
type State struct {
	Providers    []ProviderState
	Routing      RoutingState
	Sandbox      SandboxState
	Security     SecurityState
	Housekeeping HousekeepingState
}

// ProviderState describes one provider and its models.
type ProviderState struct {
	Name            string
	Kind            string
	Reachable       bool
	AuthFailed      bool
	Latency         time.Duration
	Err             string
	Concurrency     int // configured in Syntaxis (0 = unset)
	ServerParallel  int // server's real parallelism if known (0 unknown)
	Models          []ModelState
	MissingEnabled  []string // previously enabled models no longer listed
	Loaded          []provider.LoadedModel
	LoadedSupported bool
	// StreamBuffered: probe saw all chunks arrive at once (proxy buffering). nil = not probed.
	StreamBuffered *bool
	// IdleTimeout: shortest idle/response timeout in front of the provider (0 unknown).
	IdleTimeout time.Duration
}

// ModelState describes one enabled/discovered model.
type ModelState struct {
	Name            string
	Enabled         bool
	ContextAdvert   int    // advertised maximum
	ContextLoaded   int    // what the server really runs with (0 unknown)
	ContextUsed     int    // what Syntaxis uses for budgeting
	ContextSource   string // where ContextLoaded came from
	ContextOverride int    // admin override (0 none)
	ToolsSupported  *bool  // server-reported
	ToolProbe       *bool  // result of a quick tool-call probe (nil = not run)
	AliasOf         string // identical-digest group key; models sharing it should agree on context
	Digest          string
	BenchmarkDigest string // digest at last benchmark ("" = never)
	Reloads         int    // recent reload count (latency spikes)
}

// RoutingState describes the optional helper-model routing profile.
type RoutingState struct {
	Enabled  bool
	Models   []string // model names in the profile (provider-qualified by caller)
	Provider string
}

// SandboxState is the sandbox platform snapshot.
type SandboxState struct {
	DockerReachable       bool
	DockerErr             string
	RunscRegistered       bool
	SocketProxyReachable  bool
	SocketProxyUsed       bool
	SocketProxyTooOpen    bool // proxy permits dangerous endpoints (exec on host, privileged, etc.)
	RawSocketMounted      bool
	NetworkRulesApplied   bool
	ImagePresent          bool
	DiskFreePct           float64
	MemFreeMB             int
	SessionMemCapMB       int
	MaxConcurrentSessions int
}

// SecurityState is the security posture snapshot.
type SecurityState struct {
	AdminCount         int
	AdminsWith2FA      int
	RotationDays       int
	SMTPConfigured     bool
	MasterKeySet       bool
	MasterKeyInDataDir bool
	HTTPSDetected      bool
	AdminListenPublic  bool
	DefaultQuotasSet   bool
	ScanMode           string
	EgressMode         string
	ProxyAuthEnabled   bool
	ProxyTrustedEmpty  bool
	ProxyTrustsAll     bool // e.g. 0.0.0.0/0
}

// HousekeepingState is the operational hygiene snapshot.
type HousekeepingState struct {
	LastBackup   time.Time // zero = never
	RetentionSet bool
	LogDirBytes  int64
	ClockSkew    time.Duration // absolute offset against a reference; 0 unknown
	ClockChecked bool
	VAPIDSet     bool
	PushEnabled  bool
}
