// SPDX-License-Identifier: GPL-2.0-or-later

package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/audit"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// SQLite is the default Store.
type SQLite struct {
	db      *sql.DB
	auditMu sync.Mutex
}

// OpenSQLite opens (and migrates) a SQLite database at path. Use ":memory:"
// style DSNs via file::memory:?cache=shared for tests.
func OpenSQLite(path string) (*SQLite, error) {
	dsn := path
	if !strings.HasPrefix(path, "file:") {
		dsn = "file:" + path
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite has a single writer; one connection avoids SQLITE_BUSY surprises
	// and keeps in-memory databases coherent.
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate() error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.Up(s.db, "migrations")
}

// Backup writes a consistent copy of the database to dest (VACUUM INTO).
// dest must not exist.
func (s *SQLite) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup destination %q already exists", dest)
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

func ts(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromTS(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func table(kind string) (string, error) {
	switch kind {
	case KindAdmin:
		return "admins", nil
	case KindUser:
		return "users", nil
	}
	return "", fmt.Errorf("store: unknown account kind %q", kind)
}

// ---- accounts ----

// CreateAccount inserts an admin or user.
func (s *SQLite) CreateAccount(ctx context.Context, a Account) (int64, error) {
	t, err := table(a.Kind)
	if err != nil {
		return 0, err
	}
	var res sql.Result
	if a.Kind == KindAdmin {
		res, err = s.db.ExecContext(ctx, `INSERT INTO admins (email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at) VALUES (?,?,?,?,?,?,?,?)`,
			strings.ToLower(a.Email), a.Name, a.PasswordHash, a.TOTPSecret, b2i(a.TOTPEnabled), b2i(a.Disabled), ts(a.CreatedAt), ts(a.PasswordChangedAt))
	} else {
		res, err = s.db.ExecContext(ctx, `INSERT INTO `+t+` (email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at,remind_2fa_after) VALUES (?,?,?,?,?,?,?,?,?)`,
			strings.ToLower(a.Email), a.Name, a.PasswordHash, a.TOTPSecret, b2i(a.TOTPEnabled), b2i(a.Disabled), ts(a.CreatedAt), ts(a.PasswordChangedAt), ts(a.Remind2FAAfter))
	}
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}

func (s *SQLite) scanAccount(kind string, row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var created, changed, remind int64
	var totp, dis int
	a.Kind = kind
	var err error
	if kind == KindAdmin {
		err = row.Scan(&a.ID, &a.Email, &a.Name, &a.PasswordHash, &a.TOTPSecret, &totp, &dis, &created, &changed)
	} else {
		err = row.Scan(&a.ID, &a.Email, &a.Name, &a.PasswordHash, &a.TOTPSecret, &totp, &dis, &created, &changed, &remind)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	a.TOTPEnabled, a.Disabled = totp == 1, dis == 1
	a.CreatedAt, a.PasswordChangedAt, a.Remind2FAAfter = fromTS(created), fromTS(changed), fromTS(remind)
	return &a, nil
}

func acctCols(kind string) string {
	if kind == KindAdmin {
		return "id,email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at"
	}
	return "id,email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at,remind_2fa_after"
}

// GetAccountByEmail looks up by (case-insensitive) email.
func (s *SQLite) GetAccountByEmail(ctx context.Context, kind, email string) (*Account, error) {
	t, err := table(kind)
	if err != nil {
		return nil, err
	}
	return s.scanAccount(kind, s.db.QueryRowContext(ctx, `SELECT `+acctCols(kind)+` FROM `+t+` WHERE email = ?`, strings.ToLower(email)))
}

// GetAccount looks up by id.
func (s *SQLite) GetAccount(ctx context.Context, kind string, id int64) (*Account, error) {
	t, err := table(kind)
	if err != nil {
		return nil, err
	}
	return s.scanAccount(kind, s.db.QueryRowContext(ctx, `SELECT `+acctCols(kind)+` FROM `+t+` WHERE id = ?`, id))
}

// UpdateAccount updates mutable account fields.
func (s *SQLite) UpdateAccount(ctx context.Context, a Account) error {
	t, err := table(a.Kind)
	if err != nil {
		return err
	}
	var res sql.Result
	if a.Kind == KindAdmin {
		res, err = s.db.ExecContext(ctx, `UPDATE admins SET name=?,password_hash=?,totp_secret=?,totp_enabled=?,disabled=?,password_changed_at=? WHERE id=?`,
			a.Name, a.PasswordHash, a.TOTPSecret, b2i(a.TOTPEnabled), b2i(a.Disabled), ts(a.PasswordChangedAt), a.ID)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE `+t+` SET name=?,password_hash=?,totp_secret=?,totp_enabled=?,disabled=?,password_changed_at=?,remind_2fa_after=? WHERE id=?`,
			a.Name, a.PasswordHash, a.TOTPSecret, b2i(a.TOTPEnabled), b2i(a.Disabled), ts(a.PasswordChangedAt), ts(a.Remind2FAAfter), a.ID)
	}
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountAccounts counts rows of a kind.
func (s *SQLite) CountAccounts(ctx context.Context, kind string) (int, error) {
	t, err := table(kind)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n)
	return n, err
}

// ListAccounts lists accounts of a kind.
func (s *SQLite) ListAccounts(ctx context.Context, kind string) ([]Account, error) {
	t, err := table(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+acctCols(kind)+` FROM `+t+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := s.scanAccount(kind, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ---- chat-user policy ----

func (s *SQLite) scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var models string
	var cloud int
	var created, changed, remind int64
	var totp, dis int
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.TOTPSecret, &totp, &dis, &created, &changed, &remind,
		&u.QuotaDailyTokens, &u.QuotaMonthlyTokens, &u.MaxSessions, &u.EgressMode, &models, &cloud)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Kind = KindUser
	u.TOTPEnabled, u.Disabled, u.CloudToolsDisabled = totp == 1, dis == 1, cloud == 1
	u.CreatedAt, u.PasswordChangedAt, u.Remind2FAAfter = fromTS(created), fromTS(changed), fromTS(remind)
	if err := json.Unmarshal([]byte(models), &u.AllowedModels); err != nil {
		u.AllowedModels = nil
	}
	return &u, nil
}

const userCols = `id,email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at,remind_2fa_after,quota_daily_tokens,quota_monthly_tokens,max_sessions,egress_mode,allowed_models,cloud_tools_disabled`

// GetUser returns a chat user with policy fields.
func (s *SQLite) GetUser(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ListUsers returns all chat users.
func (s *SQLite) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUserPolicy updates quota/policy fields only.
func (s *SQLite) UpdateUserPolicy(ctx context.Context, u User) error {
	models, _ := json.Marshal(u.AllowedModels)
	if u.AllowedModels == nil {
		models = []byte("[]")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET quota_daily_tokens=?,quota_monthly_tokens=?,max_sessions=?,egress_mode=?,allowed_models=?,cloud_tools_disabled=? WHERE id=?`,
		u.QuotaDailyTokens, u.QuotaMonthlyTokens, u.MaxSessions, u.EgressMode, string(models), b2i(u.CloudToolsDisabled), u.ID)
	return affected(res, err)
}

// ---- recovery codes ----

// ReplaceRecoveryCodes deletes old codes and stores new hashes.
func (s *SQLite) ReplaceRecoveryCodes(ctx context.Context, kind string, id int64, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE owner_kind=? AND owner_id=?`, kind, id); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (owner_kind,owner_id,code_hash) VALUES (?,?,?)`, kind, id, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode marks a matching unused code used; reports whether it matched.
func (s *SQLite) UseRecoveryCode(ctx context.Context, kind string, id int64, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at=? WHERE owner_kind=? AND owner_id=? AND code_hash=? AND used_at=0`, time.Now().Unix(), kind, id, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CountRecoveryCodes counts unused codes.
func (s *SQLite) CountRecoveryCodes(ctx context.Context, kind string, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE owner_kind=? AND owner_id=? AND used_at=0`, kind, id).Scan(&n)
	return n, err
}

// ---- tokens ----

// PutToken stores a hashed single-use token.
func (s *SQLite) PutToken(ctx context.Context, t Token) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (hash,kind,user_id,expires_at) VALUES (?,?,?,?)`, t.Hash, t.Kind, t.UserID, ts(t.ExpiresAt))
	return mapErr(err)
}

// ConsumeToken atomically marks a valid token used and returns it.
func (s *SQLite) ConsumeToken(ctx context.Context, hash, kind string, now time.Time) (*Token, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET used_at=? WHERE hash=? AND kind=? AND used_at=0 AND expires_at>?`, now.Unix(), hash, kind, now.Unix())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	var t Token
	var exp, used int64
	if err := s.db.QueryRowContext(ctx, `SELECT hash,kind,user_id,expires_at,used_at FROM tokens WHERE hash=?`, hash).Scan(&t.Hash, &t.Kind, &t.UserID, &exp, &used); err != nil {
		return nil, mapErr(err)
	}
	t.ExpiresAt, t.UsedAt = fromTS(exp), fromTS(used)
	return &t, nil
}

// PeekToken returns a still-valid token without consuming it.
func (s *SQLite) PeekToken(ctx context.Context, hash, kind string, now time.Time) (*Token, error) {
	var t Token
	var exp, used int64
	err := s.db.QueryRowContext(ctx, `SELECT hash,kind,user_id,expires_at,used_at FROM tokens WHERE hash=? AND kind=? AND used_at=0 AND expires_at>?`, hash, kind, now.Unix()).
		Scan(&t.Hash, &t.Kind, &t.UserID, &exp, &used)
	if err != nil {
		return nil, mapErr(err)
	}
	t.ExpiresAt, t.UsedAt = fromTS(exp), fromTS(used)
	return &t, nil
}

// ---- auth sessions ----

// PutAuthSession stores a login session.
func (s *SQLite) PutAuthSession(ctx context.Context, a AuthSession) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions (id_hash,kind,owner_id,csrf,created_at,expires_at,pending_mfa,ip) VALUES (?,?,?,?,?,?,?,?)`,
		a.IDHash, a.Kind, a.OwnerID, a.CSRF, ts(a.CreatedAt), ts(a.ExpiresAt), b2i(a.PendingMFA), a.IP)
	return mapErr(err)
}

// GetAuthSession fetches by hashed id.
func (s *SQLite) GetAuthSession(ctx context.Context, idHash string) (*AuthSession, error) {
	var a AuthSession
	var c, e int64
	var p int
	err := s.db.QueryRowContext(ctx, `SELECT id_hash,kind,owner_id,csrf,created_at,expires_at,pending_mfa,ip FROM auth_sessions WHERE id_hash=?`, idHash).
		Scan(&a.IDHash, &a.Kind, &a.OwnerID, &a.CSRF, &c, &e, &p, &a.IP)
	if err != nil {
		return nil, mapErr(err)
	}
	a.CreatedAt, a.ExpiresAt, a.PendingMFA = fromTS(c), fromTS(e), p == 1
	return &a, nil
}

// UpdateAuthSession updates csrf/expiry/mfa state.
func (s *SQLite) UpdateAuthSession(ctx context.Context, a AuthSession) error {
	res, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET csrf=?,expires_at=?,pending_mfa=? WHERE id_hash=?`, a.CSRF, ts(a.ExpiresAt), b2i(a.PendingMFA), a.IDHash)
	return affected(res, err)
}

// DeleteAuthSession removes one session.
func (s *SQLite) DeleteAuthSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id_hash=?`, idHash)
	return err
}

// DeleteAuthSessionsFor revokes all sessions for an account ("log out everywhere").
func (s *SQLite) DeleteAuthSessionsFor(ctx context.Context, kind string, ownerID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE kind=? AND owner_id=?`, kind, ownerID)
	return err
}

// PurgeExpiredAuthSessions removes expired sessions.
func (s *SQLite) PurgeExpiredAuthSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- attempts ----

// GetAttempt returns the failure record for a key.
func (s *SQLite) GetAttempt(ctx context.Context, key string) (*Attempt, error) {
	var a Attempt
	var w, l int64
	err := s.db.QueryRowContext(ctx, `SELECT key,failures,window_start,locked_until FROM login_attempts WHERE key=?`, key).Scan(&a.Key, &a.Failures, &w, &l)
	if err != nil {
		return nil, mapErr(err)
	}
	a.WindowStart, a.LockedUntil = fromTS(w), fromTS(l)
	return &a, nil
}

// PutAttempt upserts a failure record.
func (s *SQLite) PutAttempt(ctx context.Context, a Attempt) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO login_attempts (key,failures,window_start,locked_until) VALUES (?,?,?,?)
ON CONFLICT(key) DO UPDATE SET failures=excluded.failures,window_start=excluded.window_start,locked_until=excluded.locked_until`,
		a.Key, a.Failures, ts(a.WindowStart), ts(a.LockedUntil))
	return err
}

// ClearAttempt removes a failure record.
func (s *SQLite) ClearAttempt(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE key=?`, key)
	return err
}

// ---- providers & models ----

// UpsertProvider inserts or updates a provider (by id, or by name when id==0).
func (s *SQLite) UpsertProvider(ctx context.Context, p Provider) (int64, error) {
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO providers (name,kind,base_url,api_key_enc,concurrency,enabled) VALUES (?,?,?,?,?,?)`,
			p.Name, p.Kind, p.BaseURL, p.APIKeyEnc, p.Concurrency, b2i(p.Enabled))
		if err != nil {
			return 0, mapErr(err)
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE providers SET name=?,kind=?,base_url=?,api_key_enc=?,concurrency=?,enabled=? WHERE id=?`,
		p.Name, p.Kind, p.BaseURL, p.APIKeyEnc, p.Concurrency, b2i(p.Enabled), p.ID)
	return p.ID, affected(res, err)
}

func scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	var p Provider
	var en int
	if err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKeyEnc, &p.Concurrency, &en); err != nil {
		return nil, mapErr(err)
	}
	p.Enabled = en == 1
	return &p, nil
}

// ListProviders returns all providers.
func (s *SQLite) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,base_url,api_key_enc,concurrency,enabled FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetProvider fetches one provider.
func (s *SQLite) GetProvider(ctx context.Context, id int64) (*Provider, error) {
	return scanProvider(s.db.QueryRowContext(ctx, `SELECT id,name,kind,base_url,api_key_enc,concurrency,enabled FROM providers WHERE id=?`, id))
}

// DeleteProvider removes a provider (cascades to models).
func (s *SQLite) DeleteProvider(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id=?`, id)
	return affected(res, err)
}

// UpsertModel inserts/updates discovery fields; it never overwrites the admin's enabled flag.
func (s *SQLite) UpsertModel(ctx context.Context, m Model) (int64, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO models (provider_id,name,enabled,context_override,digest,tools_supported,recommended,seen_at) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(provider_id,name) DO UPDATE SET digest=excluded.digest,tools_supported=excluded.tools_supported,seen_at=excluded.seen_at`,
		m.ProviderID, m.Name, b2i(m.Enabled), m.ContextOverride, m.Digest, m.ToolsSupported, b2i(m.Recommended), ts(m.SeenAt))
	if err != nil {
		return 0, mapErr(err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM models WHERE provider_id=? AND name=?`, m.ProviderID, m.Name).Scan(&id)
	return id, err
}

// ListModels lists models (providerID 0 = all).
func (s *SQLite) ListModels(ctx context.Context, providerID int64) ([]Model, error) {
	q := `SELECT id,provider_id,name,enabled,context_override,digest,tools_supported,recommended,seen_at FROM models`
	args := []any{}
	if providerID != 0 {
		q += ` WHERE provider_id=?`
		args = append(args, providerID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY provider_id,name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var en, rec int
		var seen int64
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.Name, &en, &m.ContextOverride, &m.Digest, &m.ToolsSupported, &rec, &seen); err != nil {
			return nil, err
		}
		m.Enabled, m.Recommended, m.SeenAt = en == 1, rec == 1, fromTS(seen)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetModelEnabled toggles a model.
func (s *SQLite) SetModelEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE models SET enabled=? WHERE id=?`, b2i(enabled), id)
	return affected(res, err)
}

// SetModelRecommended sets the admin's "recommended for agents" flag.
func (s *SQLite) SetModelRecommended(ctx context.Context, id int64, rec bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE models SET recommended=? WHERE id=?`, b2i(rec), id)
	return affected(res, err)
}

// GetModel fetches one model.
func (s *SQLite) GetModel(ctx context.Context, id int64) (*Model, error) {
	ms, err := s.ListModels(ctx, 0)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i], nil
		}
	}
	return nil, ErrNotFound
}

// ---- chat sessions & messages ----

// CreateChatSession inserts a session.
func (s *SQLite) CreateChatSession(ctx context.Context, c ChatSession) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_sessions (id,user_id,title,provider_id,model,autonomy,state,sandbox_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.UserID, c.Title, c.ProviderID, c.Model, c.Autonomy, c.State, c.SandboxID, ts(c.CreatedAt), ts(c.UpdatedAt))
	return mapErr(err)
}

const sessCols = `id,user_id,title,provider_id,model,autonomy,state,sandbox_id,created_at,updated_at,tokens_in,tokens_out`

func scanSession(row interface{ Scan(...any) error }) (*ChatSession, error) {
	var c ChatSession
	var cr, up int64
	if err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.ProviderID, &c.Model, &c.Autonomy, &c.State, &c.SandboxID, &cr, &up, &c.TokensIn, &c.TokensOut); err != nil {
		return nil, mapErr(err)
	}
	c.CreatedAt, c.UpdatedAt = fromTS(cr), fromTS(up)
	return &c, nil
}

// GetChatSession fetches a non-deleted session.
func (s *SQLite) GetChatSession(ctx context.Context, id string) (*ChatSession, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessCols+` FROM chat_sessions WHERE id=? AND deleted=0`, id))
}

func (s *SQLite) listSessions(ctx context.Context, where string, args ...any) ([]ChatSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessCols+` FROM chat_sessions WHERE deleted=0 `+where+` ORDER BY updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatSession
	for rows.Next() {
		c, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListChatSessions lists one user's sessions.
func (s *SQLite) ListChatSessions(ctx context.Context, userID int64) ([]ChatSession, error) {
	return s.listSessions(ctx, `AND user_id=?`, userID)
}

// ListAllChatSessions lists every session (admin metadata view).
func (s *SQLite) ListAllChatSessions(ctx context.Context) ([]ChatSession, error) {
	return s.listSessions(ctx, ``)
}

// UpdateChatSession updates mutable fields.
func (s *SQLite) UpdateChatSession(ctx context.Context, c ChatSession) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET title=?,provider_id=?,model=?,autonomy=?,state=?,sandbox_id=?,updated_at=?,tokens_in=?,tokens_out=? WHERE id=? AND deleted=0`,
		c.Title, c.ProviderID, c.Model, c.Autonomy, c.State, c.SandboxID, ts(c.UpdatedAt), c.TokensIn, c.TokensOut, c.ID)
	return affected(res, err)
}

// DeleteChatSession soft-deletes a session (history purge is a lifecycle job).
func (s *SQLite) DeleteChatSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET deleted=1 WHERE id=? AND deleted=0`, id)
	return affected(res, err)
}

// AppendMessage stores a message.
func (s *SQLite) AppendMessage(ctx context.Context, m Message) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages (session_id,branch,abandoned,role,content,tool_calls,tool_call_id,tokens_in,tokens_out,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		m.SessionID, m.Branch, b2i(m.Abandoned), m.Role, m.Content, m.ToolCalls, m.ToolCallID, m.TokensIn, m.TokensOut, ts(m.CreatedAt))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}

// ListMessages returns messages in order.
func (s *SQLite) ListMessages(ctx context.Context, sessionID string, includeAbandoned bool) ([]Message, error) {
	q := `SELECT id,session_id,branch,abandoned,role,content,tool_calls,tool_call_id,tokens_in,tokens_out,created_at FROM messages WHERE session_id=?`
	if !includeAbandoned {
		q += ` AND abandoned=0`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ab int
		var cr int64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Branch, &ab, &m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID, &m.TokensIn, &m.TokensOut, &cr); err != nil {
			return nil, err
		}
		m.Abandoned, m.CreatedAt = ab == 1, fromTS(cr)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AbandonMessagesAfter marks later messages as an abandoned branch (rewind).
func (s *SQLite) AbandonMessagesAfter(ctx context.Context, sessionID string, afterID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET abandoned=1 WHERE session_id=? AND id>? AND abandoned=0`, sessionID, afterID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- usage ----

// AddUsage accumulates daily usage.
func (s *SQLite) AddUsage(ctx context.Context, userID int64, day string, in, out, sandboxSeconds int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_daily (user_id,day,tokens_in,tokens_out,sandbox_seconds) VALUES (?,?,?,?,?)
ON CONFLICT(user_id,day) DO UPDATE SET tokens_in=tokens_in+excluded.tokens_in,tokens_out=tokens_out+excluded.tokens_out,sandbox_seconds=sandbox_seconds+excluded.sandbox_seconds`,
		userID, day, in, out, sandboxSeconds)
	return err
}

// GetUsage returns one day's usage (zeros when absent).
func (s *SQLite) GetUsage(ctx context.Context, userID int64, day string) (*Usage, error) {
	u := Usage{UserID: userID, Day: day}
	err := s.db.QueryRowContext(ctx, `SELECT tokens_in,tokens_out,sandbox_seconds FROM usage_daily WHERE user_id=? AND day=?`, userID, day).Scan(&u.TokensIn, &u.TokensOut, &u.SandboxSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return &u, nil
	}
	return &u, err
}

// SumUsage sums usage for days starting with dayPrefix (e.g. "2026-11" for a month).
func (s *SQLite) SumUsage(ctx context.Context, userID int64, dayPrefix string) (*Usage, error) {
	u := Usage{UserID: userID, Day: dayPrefix}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(sandbox_seconds),0) FROM usage_daily WHERE user_id=? AND day LIKE ?`, userID, dayPrefix+"%").
		Scan(&u.TokensIn, &u.TokensOut, &u.SandboxSeconds)
	return &u, err
}

// ResetUsage clears one day's usage (admin reset).
func (s *SQLite) ResetUsage(ctx context.Context, userID int64, day string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM usage_daily WHERE user_id=? AND day=?`, userID, day)
	return err
}

// ---- secrets ----

// PutSecret upserts an encrypted secret.
func (s *SQLite) PutSecret(ctx context.Context, sc Secret) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets (scope,owner_id,name,ciphertext,created_at) VALUES (?,?,?,?,?)
ON CONFLICT(scope,owner_id,name) DO UPDATE SET ciphertext=excluded.ciphertext`, sc.Scope, sc.OwnerID, sc.Name, sc.Ciphertext, ts(sc.CreatedAt))
	return err
}

// GetSecret fetches a secret by scope/owner/name. Owner is always part of the key.
func (s *SQLite) GetSecret(ctx context.Context, scope string, ownerID int64, name string) (*Secret, error) {
	var sc Secret
	var c int64
	err := s.db.QueryRowContext(ctx, `SELECT id,scope,owner_id,name,ciphertext,created_at FROM secrets WHERE scope=? AND owner_id=? AND name=?`, scope, ownerID, name).
		Scan(&sc.ID, &sc.Scope, &sc.OwnerID, &sc.Name, &sc.Ciphertext, &c)
	if err != nil {
		return nil, mapErr(err)
	}
	sc.CreatedAt = fromTS(c)
	return &sc, nil
}

// ListSecrets lists secrets for an owner.
func (s *SQLite) ListSecrets(ctx context.Context, scope string, ownerID int64) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope,owner_id,name,ciphertext,created_at FROM secrets WHERE scope=? AND owner_id=? ORDER BY name`, scope, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sc Secret
		var c int64
		if err := rows.Scan(&sc.ID, &sc.Scope, &sc.OwnerID, &sc.Name, &sc.Ciphertext, &c); err != nil {
			return nil, err
		}
		sc.CreatedAt = fromTS(c)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DeleteSecret removes a secret.
func (s *SQLite) DeleteSecret(ctx context.Context, scope string, ownerID int64, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE scope=? AND owner_id=? AND name=?`, scope, ownerID, name)
	return affected(res, err)
}

// ---- audit ----

// AppendAudit appends a hash-chained entry. Serialised so the chain is linear.
func (s *SQLite) AppendAudit(ctx context.Context, actor, action, target, detail string) (audit.Entry, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	prev := audit.GenesisHash
	var lastSeq int64
	err := s.db.QueryRowContext(ctx, `SELECT seq,hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return audit.Entry{}, err
	}
	now := time.Now().UTC()
	e := audit.Seal(audit.Entry{Seq: lastSeq + 1, Time: now, Actor: actor, Action: action, Target: target, Detail: detail}, prev)
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log (seq,time,time_nanos,actor,action,target,detail,prev_hash,hash) VALUES (?,?,?,?,?,?,?,?,?)`,
		e.Seq, now.Unix(), now.Nanosecond(), actor, action, target, detail, e.PrevHash, e.Hash)
	return e, err
}

// ListAudit returns the oldest `limit` entries (0 = all) in chain order.
func (s *SQLite) ListAudit(ctx context.Context, limit int) ([]audit.Entry, error) {
	q := `SELECT seq,time,time_nanos,actor,action,target,detail,prev_hash,hash FROM audit_log ORDER BY seq`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Entry
	for rows.Next() {
		var e audit.Entry
		var t, n int64
		if err := rows.Scan(&e.Seq, &t, &n, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		e.Time = time.Unix(t, n).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- egress ----

// AddEgress records network metadata.
func (s *SQLite) AddEgress(ctx context.Context, e EgressEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO egress_log (time,user_id,session_id,dest_host,dest_ip,port,protocol,bytes,allowed,dns) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		ts(e.Time), e.UserID, e.SessionID, e.DestHost, e.DestIP, e.Port, e.Protocol, e.Bytes, b2i(e.Allowed), b2i(e.DNS))
	return err
}

// ListEgress lists newest entries. userID 0 = all users (admin); otherwise only that user's.
func (s *SQLite) ListEgress(ctx context.Context, userID int64, limit int) ([]EgressEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT id,time,user_id,session_id,dest_host,dest_ip,port,protocol,bytes,allowed,dns FROM egress_log`
	args := []any{}
	if userID != 0 {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressEntry
	for rows.Next() {
		var e EgressEntry
		var t int64
		var al, dn int
		if err := rows.Scan(&e.ID, &t, &e.UserID, &e.SessionID, &e.DestHost, &e.DestIP, &e.Port, &e.Protocol, &e.Bytes, &al, &dn); err != nil {
			return nil, err
		}
		e.Time, e.Allowed, e.DNS = fromTS(t), al == 1, dn == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- benchmarks ----

// AddBenchmark stores a result.
func (s *SQLite) AddBenchmark(ctx context.Context, modelID int64, digest, resultJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO benchmarks (model_id,time,digest,result) VALUES (?,?,?,?)`, modelID, time.Now().Unix(), digest, resultJSON)
	return err
}

// ListBenchmarks returns results for a model (0 = all), newest first.
func (s *SQLite) ListBenchmarks(ctx context.Context, modelID int64) ([]Benchmark, error) {
	q := `SELECT id,model_id,time,digest,result FROM benchmarks`
	args := []any{}
	if modelID != 0 {
		q += ` WHERE model_id=?`
		args = append(args, modelID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Benchmark
	for rows.Next() {
		var b Benchmark
		var t int64
		if err := rows.Scan(&b.ID, &b.ModelID, &t, &b.Digest, &b.Result); err != nil {
			return nil, err
		}
		b.Time = fromTS(t)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- settings ----

// GetSetting returns a setting or def.
func (s *SQLite) GetSetting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting upserts a setting.
func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

var _ Store = (*SQLite)(nil)
