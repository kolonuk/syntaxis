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
