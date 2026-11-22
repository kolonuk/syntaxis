// SPDX-License-Identifier: GPL-2.0-or-later

// Package store defines persistence behind the Store interface. The default
// implementation is SQLite (pure Go, WAL). SQL is kept portable (standard
// syntax, `?` placeholders rebound per dialect) so PostgreSQL can be added.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/kolonuk/syntaxis/internal/audit"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned on unique-constraint violations.
var ErrConflict = errors.New("store: conflict")

// Account kinds. Admins and chat users live in separate tables on purpose.
const (
	KindAdmin = "admin"
	KindUser  = "user"
)

// Account is the shared shape of an admin or chat user for auth code.
type Account struct {
	Kind              string
	ID                int64
	Email             string
	Name              string
	PasswordHash      string
	TOTPSecret        []byte // encrypted by caller
	TOTPEnabled       bool
	Disabled          bool
	CreatedAt         time.Time
	PasswordChangedAt time.Time
	Remind2FAAfter    time.Time // users only
}

// User is a chat user including quota/policy fields.
type User struct {
	Account
	QuotaDailyTokens   int64
	QuotaMonthlyTokens int64
	MaxSessions        int
	EgressMode         string // "" = instance default
	AllowedModels      []string
	CloudToolsDisabled bool
	TerminalDisabled   bool // admin switch: this user may not open an interactive terminal
	QuotaCPUMinutes    int64
	Level              string // "user" | "poweruser" | "admin"
	GroupID            int64  // 0 = no group
}

// User levels.
const (
	LevelUser      = "user"
	LevelPowerUser = "poweruser"
	LevelAdmin     = "admin"
)

// PolicyData is a set of permissions and settings that a level or a group can carry. Every
// field is optional: nil/zero means "not set here", and the next layer decides.
type PolicyData struct {
	DailyTokens   int64    `json:"daily_tokens,omitempty"`
	MonthlyTokens int64    `json:"monthly_tokens,omitempty"`
	MaxSessions   int      `json:"max_sessions,omitempty"`
	CPUMinutes    int64    `json:"cpu_minutes,omitempty"`
	EgressMode    string   `json:"egress_mode,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	NoTerminal    bool     `json:"no_terminal,omitempty"`
	NoCloudTools  bool     `json:"no_cloud_tools,omitempty"`
}

// Connection is the record of one user's connection to an external service. The credentials
// themselves are secrets in the user's vault; Settings holds the non-secret fields.
type Connection struct {
	UserID    int64
	Kind      string
	Account   string
	Detail    string
	Settings  map[string]string
	CheckedAt time.Time
}

// Automation starts a run by itself: on a schedule, when a webhook is called, or when a watch finds
// something new. It belongs to one user and runs as them, with their limits.
type Automation struct {
	ID         string
	UserID     int64
	Name       string
	Enabled    bool
	Trigger    string // "schedule" | "webhook" | "watch"
	Config     map[string]string
	Prompt     string
	Model      string
	Autonomy   string
	Repo       string // JSON repository spec, or ""
	Notify     map[string]string
	TokenHash  string
	State      string
	NextRunAt  time.Time
	LastRunAt  time.Time
	LastStatus string
	LastError  string
	FailCount  int
	CreatedAt  time.Time
}

// AutomationRun is one start of an automation.
type AutomationRun struct {
	ID           int64
	AutomationID string
	SessionID    string
	StartedAt    time.Time
	Source       string
	Detail       string
}

// APIToken is a personal API token. The token itself is never stored, only its hash.
type APIToken struct {
	ID         int64
	UserID     int64
	Name       string
	Prefix     string // the first characters, to recognise it in a list
	Scope      string // "read" or "run"
	CreatedAt  time.Time
	ExpiresAt  time.Time // zero = does not expire
	LastUsedAt time.Time
	RevokedAt  time.Time
}

// Share is a disposable read-only link to a session.
type Share struct {
	ID           string
	SessionID    string
	UserID       int64
	Name         string
	TokenHash    string
	TokenEnc     []byte
	IncludeTools bool
	CreatedAt    time.Time
	ExpiresAt    time.Time
	Revoked      bool
	Views        int
	LastViewAt   time.Time
}

// CheckpointMark ties a workspace checkpoint to the point in a conversation where it was taken, so
// restoring can also rewind the conversation.
type CheckpointMark struct {
	UserID    int64
	Seq       int
	SessionID string
	MessageID int64
	Label     string
	CreatedAt time.Time
}

// Policy is a named PolicyData: a user level ("level") or a user group ("group").
type Policy struct {
	ID   int64
	Kind string // "level" | "group"
	Name string
	Data PolicyData
}

// AuthSession is a server-side login session.
type AuthSession struct {
	IDHash     string
	Kind       string
	OwnerID    int64
	CSRF       string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	PendingMFA bool
	IP         string
}

// Token is a single-use invite/reset token (stored hashed).
type Token struct {
	Hash      string
	Kind      string
	UserID    int64
	ExpiresAt time.Time
	UsedAt    time.Time
}

// Attempt tracks failed logins for rate limiting.
type Attempt struct {
	Key         string
	Failures    int
	WindowStart time.Time
	LockedUntil time.Time
}

// Provider is a configured LLM provider.
type Provider struct {
	ID          int64
	Name        string
	Kind        string
	BaseURL     string
	APIKeyEnc   []byte
	Concurrency int
	Enabled     bool
}

// ImageTemplate is an environment an admin has approved.
type ImageTemplate struct {
	Name        string
	Image       string
	Matches     []string
	Description string
	Managed     bool      // built from the catalog by Syntaxis, so it may be removed when unused
	LastUsed    time.Time // when a session last started in it (zero = never)
}

// Probe is the result of a live check of a model server.
type Probe struct {
	ProviderID int64
	Model      string // "" for a result about the provider
	Kind       string // "tools" | "streaming"
	OK         bool
	Detail     string
	At         time.Time
}

// Model is a discovered model on a provider.
type Model struct {
	ID              int64
	ProviderID      int64
	Name            string
	Enabled         bool
	ContextOverride int
	Digest          string
	ToolsSupported  int // -1 unknown, 0 no, 1 yes
	Recommended     bool
	SeenAt          time.Time
}

// ChatSession is one agent session.
type ChatSession struct {
	ID         string
	UserID     int64
	Title      string
	ProviderID int64
	Model      string
	Autonomy   string
	State      string
	SandboxID  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	TokensIn   int64
	TokensOut  int64
	// Repo is the repository this session works on, if it was started from one.
	RepoKind   string // "github", "gitlab", "gitea"
	RepoName   string // owner/name
	RepoURL    string // https clone address
	RepoBranch string // the working branch the agent commits to
	RepoBase   string // the branch to merge into
	// PlanMode makes the agent propose a plan first; nothing is changed until the user approves it.
	PlanMode  bool
	PlanState string // "" | "proposed" | "approved"
	PlanText  string
	// BatchID groups attempts at the same task that were started together to be compared.
	BatchID string
	// Image is the sandbox image this session runs in ("" = the default); EnvNote says why.
	Image, EnvNote string
}

// MemoryNote is a note the agent keeps across sessions.
type MemoryNote struct {
	ID        int64
	UserID    int64
	Scope     string // "user" or "repo"
	ScopeKey  string // owner/name for a repo note
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Message is a stored conversation message.
type Message struct {
	ID         int64
	SessionID  string
	Branch     int
	Abandoned  bool
	Role       string
	Content    string
	ToolCalls  string
	ToolCallID string
	TokensIn   int
	TokensOut  int
	CreatedAt  time.Time
}

// Usage is a per-day token usage row.
type Usage struct {
	UserID         int64
	Day            string
	TokensIn       int64
	TokensOut      int64
	SandboxSeconds int64
	CPUSeconds     int64 // CPU time actually used by the sandbox processes
}

// Secret is an encrypted secret row.
type Secret struct {
	ID         int64
	Scope      string // "instance" | "user"
	OwnerID    int64
	Name       string
	Ciphertext []byte
	CreatedAt  time.Time
}

// EgressEntry is a sandbox network metadata record.
type EgressEntry struct {
	ID        int64
	Time      time.Time
	UserID    int64
	SessionID string
	DestHost  string
	DestIP    string
	Port      int
	Protocol  string
	Bytes     int64
	Allowed   bool
	DNS       bool
}

// Benchmark is a stored benchmark result (admin only).
type Benchmark struct {
	ID      int64
	ModelID int64
	Time    time.Time
	Digest  string
	Result  string
}

// Store is the persistence interface.
type Store interface {
	Close() error

	// accounts (kind selects admins or users table)
	CreateAccount(ctx context.Context, a Account) (int64, error)
	GetAccountByEmail(ctx context.Context, kind, email string) (*Account, error)
	GetAccount(ctx context.Context, kind string, id int64) (*Account, error)
	UpdateAccount(ctx context.Context, a Account) error
	CountAccounts(ctx context.Context, kind string) (int, error)
	ListAccounts(ctx context.Context, kind string) ([]Account, error)

	// chat-user policy
	GetUser(ctx context.Context, id int64) (*User, error)
	UpdateUserPolicy(ctx context.Context, u User) error
	ListUsers(ctx context.Context) ([]User, error)

	// automations
	CreateAutomation(ctx context.Context, a Automation) error
	UpdateAutomation(ctx context.Context, a Automation) error
	GetAutomation(ctx context.Context, id string) (*Automation, error)
	ListAutomations(ctx context.Context, userID int64) ([]Automation, error)
	DueAutomations(ctx context.Context, now time.Time, limit int) ([]Automation, error)
	DeleteAutomation(ctx context.Context, id string) error
	AddAutomationRun(ctx context.Context, r AutomationRun) error
	ListAutomationRuns(ctx context.Context, automationID string, limit int) ([]AutomationRun, error)
	CountAutomationRunsSince(ctx context.Context, automationID string, since time.Time) (int, error)

	// checkpoint marks
	PutCheckpointMark(ctx context.Context, m CheckpointMark) error
	GetCheckpointMark(ctx context.Context, userID int64, seq int) (*CheckpointMark, error)
	ListCheckpointMarks(ctx context.Context, userID int64) ([]CheckpointMark, error)

	// shared session links
	CreateAPIToken(ctx context.Context, t APIToken, hash string) (int64, error)
	GetAPITokenByHash(ctx context.Context, hash string) (*APIToken, error)
	ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error)
	RevokeAPIToken(ctx context.Context, userID, id int64, at time.Time) error
	TouchAPIToken(ctx context.Context, id int64, at time.Time) error
	CreateShare(ctx context.Context, s Share) error
	GetShareByHash(ctx context.Context, hash string) (*Share, error)
	GetShare(ctx context.Context, id string) (*Share, error)
	ListShares(ctx context.Context, userID int64) ([]Share, error)
	RevokeShare(ctx context.Context, id string) error
	RecordShareView(ctx context.Context, id string, at time.Time) error
	CountActiveShares(ctx context.Context, userID int64, now time.Time) (int, error)

	// connections to external services (per user)
	ListConnections(ctx context.Context, userID int64) ([]Connection, error)
	PutConnection(ctx context.Context, c Connection) error
	DeleteConnection(ctx context.Context, userID int64, kind string) error

	// levels and groups
	ListPolicies(ctx context.Context, kind string) ([]Policy, error)
	GetPolicy(ctx context.Context, kind, name string) (*Policy, error)
	GetPolicyByID(ctx context.Context, id int64) (*Policy, error)
	UpsertPolicy(ctx context.Context, p Policy) (int64, error)
	DeletePolicy(ctx context.Context, id int64) error

	// recovery codes
	ReplaceRecoveryCodes(ctx context.Context, kind string, id int64, hashes []string) error
	UseRecoveryCode(ctx context.Context, kind string, id int64, hash string) (bool, error)
	CountRecoveryCodes(ctx context.Context, kind string, id int64) (int, error)

	// single-use tokens
	PutToken(ctx context.Context, t Token) error
	ConsumeToken(ctx context.Context, hash, kind string, now time.Time) (*Token, error)
	PeekToken(ctx context.Context, hash, kind string, now time.Time) (*Token, error)

	// auth sessions
	PutAuthSession(ctx context.Context, s AuthSession) error
	GetAuthSession(ctx context.Context, idHash string) (*AuthSession, error)
	UpdateAuthSession(ctx context.Context, s AuthSession) error
	DeleteAuthSession(ctx context.Context, idHash string) error
	DeleteAuthSessionsFor(ctx context.Context, kind string, ownerID int64) error
	PurgeExpiredAuthSessions(ctx context.Context, now time.Time) (int64, error)

	// login attempts
	GetAttempt(ctx context.Context, key string) (*Attempt, error)
	PutAttempt(ctx context.Context, a Attempt) error
	ClearAttempt(ctx context.Context, key string) error

	// providers + models
	UpsertProvider(ctx context.Context, p Provider) (int64, error)
	SetProbe(ctx context.Context, p Probe) error
	// memory notes (every call is scoped to one user)
	ListMemory(ctx context.Context, userID int64) ([]MemoryNote, error)
	AddMemory(ctx context.Context, n MemoryNote) (int64, error)
	UpdateMemory(ctx context.Context, userID, id int64, content string, at time.Time) error
	DeleteMemory(ctx context.Context, userID, id int64) error
	ListImageTemplates(ctx context.Context) ([]ImageTemplate, error)
	UpsertImageTemplate(ctx context.Context, t ImageTemplate) error
	DeleteImageTemplate(ctx context.Context, name string) error
	TouchImageTemplate(ctx context.Context, image string, at time.Time) error
	ImageInUse(ctx context.Context, image string) (bool, error)
	ListProbes(ctx context.Context) ([]Probe, error)
	ListProviders(ctx context.Context) ([]Provider, error)
	GetProvider(ctx context.Context, id int64) (*Provider, error)
	DeleteProvider(ctx context.Context, id int64) error
	UpsertModel(ctx context.Context, m Model) (int64, error)
	ListModels(ctx context.Context, providerID int64) ([]Model, error)
	SetModelEnabled(ctx context.Context, id int64, enabled bool) error
	SetModelRecommended(ctx context.Context, id int64, rec bool) error
	GetModel(ctx context.Context, id int64) (*Model, error)

	// chat sessions + messages
	CreateChatSession(ctx context.Context, s ChatSession) error
	GetChatSession(ctx context.Context, id string) (*ChatSession, error)
	ListChatSessions(ctx context.Context, userID int64) ([]ChatSession, error)
	ListAllChatSessions(ctx context.Context) ([]ChatSession, error)
	UpdateChatSession(ctx context.Context, s ChatSession) error
	DeleteChatSession(ctx context.Context, id string) error
	AppendMessage(ctx context.Context, m Message) (int64, error)
	ListMessages(ctx context.Context, sessionID string, includeAbandoned bool) ([]Message, error)
	AbandonMessagesAfter(ctx context.Context, sessionID string, afterID int64) (int64, error)

	// usage
	AddUsage(ctx context.Context, userID int64, day string, in, out, sandboxSeconds int64) error
	// AddCPUSeconds accumulates CPU time used by a user's sandboxes today.
	AddCPUSeconds(ctx context.Context, userID int64, day string, seconds int64) error
	GetUsage(ctx context.Context, userID int64, day string) (*Usage, error)
	SumUsage(ctx context.Context, userID int64, dayPrefix string) (*Usage, error)
	ResetUsage(ctx context.Context, userID int64, day string) error

	// secrets
	PutSecret(ctx context.Context, s Secret) error
	GetSecret(ctx context.Context, scope string, ownerID int64, name string) (*Secret, error)
	ListSecrets(ctx context.Context, scope string, ownerID int64) ([]Secret, error)
	DeleteSecret(ctx context.Context, scope string, ownerID int64, name string) error

	// audit (hash chained)
	AppendAudit(ctx context.Context, actor, action, target, detail string) (audit.Entry, error)
	ListAudit(ctx context.Context, limit int) ([]audit.Entry, error)

	// egress log
	AddEgress(ctx context.Context, e EgressEntry) error
	ListEgress(ctx context.Context, userID int64, limit int) ([]EgressEntry, error)

	// benchmarks
	AddBenchmark(ctx context.Context, modelID int64, digest, resultJSON string) error
	ListBenchmarks(ctx context.Context, modelID int64) ([]Benchmark, error)

	// settings
	GetSetting(ctx context.Context, key, def string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}
