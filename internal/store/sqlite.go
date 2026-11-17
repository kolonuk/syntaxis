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

func table(kind string) (string, error) { // returns only fixed table names: safe to concatenate
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
	var cloud, term int
	var created, changed, remind int64
	var totp, dis int
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.TOTPSecret, &totp, &dis, &created, &changed, &remind,
		&u.QuotaDailyTokens, &u.QuotaMonthlyTokens, &u.MaxSessions, &u.EgressMode, &models, &cloud, &term, &u.QuotaCPUMinutes, &u.Level, &u.GroupID)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Kind = KindUser
	u.TOTPEnabled, u.Disabled, u.CloudToolsDisabled, u.TerminalDisabled = totp == 1, dis == 1, cloud == 1, term == 1
	u.CreatedAt, u.PasswordChangedAt, u.Remind2FAAfter = fromTS(created), fromTS(changed), fromTS(remind)
	if err := json.Unmarshal([]byte(models), &u.AllowedModels); err != nil {
		u.AllowedModels = nil
	}
	return &u, nil
}

const userCols = `id,email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at,remind_2fa_after,quota_daily_tokens,quota_monthly_tokens,max_sessions,egress_mode,allowed_models,cloud_tools_disabled,terminal_disabled,quota_cpu_minutes,level,group_id`

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
	res, err := s.db.ExecContext(ctx, `UPDATE users SET quota_daily_tokens=?,quota_monthly_tokens=?,max_sessions=?,egress_mode=?,allowed_models=?,cloud_tools_disabled=?,terminal_disabled=?,quota_cpu_minutes=?,level=?,group_id=? WHERE id=?`,
		u.QuotaDailyTokens, u.QuotaMonthlyTokens, u.MaxSessions, u.EgressMode, string(models), b2i(u.CloudToolsDisabled), b2i(u.TerminalDisabled), u.QuotaCPUMinutes, levelOrUser(u.Level), u.GroupID, u.ID)
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

// ListImageTemplates returns the approved environments.
func (s *SQLite) ListImageTemplates(ctx context.Context) ([]ImageTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,image,matches,description,managed,last_used FROM image_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageTemplate
	for rows.Next() {
		var t ImageTemplate
		var m string
		var managed int
		var used int64
		if err := rows.Scan(&t.Name, &t.Image, &m, &t.Description, &managed, &used); err != nil {
			return nil, err
		}
		t.Managed = managed == 1
		if used > 0 {
			t.LastUsed = fromTS(used)
		}
		for _, x := range strings.Split(m, "\n") {
			if x = strings.TrimSpace(x); x != "" {
				t.Matches = append(t.Matches, x)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertImageTemplate adds or replaces an approved environment.
func (s *SQLite) UpsertImageTemplate(ctx context.Context, t ImageTemplate) error {
	var used int64
	if !t.LastUsed.IsZero() {
		used = ts(t.LastUsed)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO image_templates (name,image,matches,description,managed,last_used) VALUES (?,?,?,?,?,?)
ON CONFLICT(name) DO UPDATE SET image=excluded.image,matches=excluded.matches,description=excluded.description,managed=excluded.managed`,
		t.Name, t.Image, strings.Join(t.Matches, "\n"), t.Description, b2i(t.Managed), used)
	return mapErr(err)
}

// ImageInUse reports whether a session that has not been deleted still has a sandbox in this image.
func (s *SQLite) ImageInUse(ctx context.Context, image string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_sessions WHERE image=? AND sandbox_id!='' AND deleted=0`, image).Scan(&n)
	return n > 0, err
}

// TouchImageTemplate records that a session started in an image.
func (s *SQLite) TouchImageTemplate(ctx context.Context, image string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE image_templates SET last_used=? WHERE image=?`, ts(at), image)
	return mapErr(err)
}

// DeleteImageTemplate removes an approved environment.
func (s *SQLite) DeleteImageTemplate(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM image_templates WHERE name=?`, name)
	return affected(res, err)
}

// SetProbe records a probe result, replacing the previous one of the same kind.
func (s *SQLite) SetProbe(ctx context.Context, p Probe) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO probes (provider_id,model,kind,result,detail,at) VALUES (?,?,?,?,?,?)
ON CONFLICT(provider_id,model,kind) DO UPDATE SET result=excluded.result,detail=excluded.detail,at=excluded.at`,
		p.ProviderID, p.Model, p.Kind, b2i(p.OK), p.Detail, ts(p.At))
	return mapErr(err)
}

// ListProbes returns every stored probe result.
func (s *SQLite) ListProbes(ctx context.Context) ([]Probe, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,model,kind,result,detail,at FROM probes ORDER BY provider_id,model,kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Probe
	for rows.Next() {
		var p Probe
		var ok int
		var at int64
		if err := rows.Scan(&p.ProviderID, &p.Model, &p.Kind, &ok, &p.Detail, &at); err != nil {
			return nil, err
		}
		p.OK, p.At = ok == 1, fromTS(at)
		out = append(out, p)
	}
	return out, rows.Err()
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_sessions (id,user_id,title,provider_id,model,autonomy,state,sandbox_id,created_at,updated_at,repo_kind,repo_name,repo_url,repo_branch,repo_base,plan_mode,plan_state,plan_text,batch_id,image,env_note) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.UserID, c.Title, c.ProviderID, c.Model, c.Autonomy, c.State, c.SandboxID, ts(c.CreatedAt), ts(c.UpdatedAt), c.RepoKind, c.RepoName, c.RepoURL, c.RepoBranch, c.RepoBase, b2i(c.PlanMode), c.PlanState, c.PlanText, c.BatchID, c.Image, c.EnvNote)
	return mapErr(err)
}

const sessCols = `id,user_id,title,provider_id,model,autonomy,state,sandbox_id,created_at,updated_at,tokens_in,tokens_out,repo_kind,repo_name,repo_url,repo_branch,repo_base,plan_mode,plan_state,plan_text,batch_id,image,env_note`

func scanSession(row interface{ Scan(...any) error }) (*ChatSession, error) {
	var c ChatSession
	var cr, up int64
	var pm int
	if err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.ProviderID, &c.Model, &c.Autonomy, &c.State, &c.SandboxID, &cr, &up, &c.TokensIn, &c.TokensOut, &c.RepoKind, &c.RepoName, &c.RepoURL, &c.RepoBranch, &c.RepoBase, &pm, &c.PlanState, &c.PlanText, &c.BatchID, &c.Image, &c.EnvNote); err != nil {
		return nil, mapErr(err)
	}
	c.CreatedAt, c.UpdatedAt = fromTS(cr), fromTS(up)
	c.PlanMode = pm == 1
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
	res, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET title=?,provider_id=?,model=?,autonomy=?,state=?,sandbox_id=?,updated_at=?,tokens_in=?,tokens_out=?,plan_state=?,plan_text=? WHERE id=? AND deleted=0`,
		c.Title, c.ProviderID, c.Model, c.Autonomy, c.State, c.SandboxID, ts(c.UpdatedAt), c.TokensIn, c.TokensOut, c.PlanState, c.PlanText, c.ID)
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

// AddCPUSeconds accumulates daily CPU time.
func (s *SQLite) AddCPUSeconds(ctx context.Context, userID int64, day string, seconds int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_daily (user_id,day,cpu_seconds) VALUES (?,?,?)
ON CONFLICT(user_id,day) DO UPDATE SET cpu_seconds=cpu_seconds+excluded.cpu_seconds`, userID, day, seconds)
	return err
}

// GetUsage returns one day's usage (zeros when absent).
func (s *SQLite) GetUsage(ctx context.Context, userID int64, day string) (*Usage, error) {
	u := Usage{UserID: userID, Day: day}
	err := s.db.QueryRowContext(ctx, `SELECT tokens_in,tokens_out,sandbox_seconds,cpu_seconds FROM usage_daily WHERE user_id=? AND day=?`, userID, day).Scan(&u.TokensIn, &u.TokensOut, &u.SandboxSeconds, &u.CPUSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return &u, nil
	}
	return &u, err
}

// SumUsage sums usage for days starting with dayPrefix (e.g. "2026-11" for a month).
func (s *SQLite) SumUsage(ctx context.Context, userID int64, dayPrefix string) (*Usage, error) {
	u := Usage{UserID: userID, Day: dayPrefix}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_in),0),COALESCE(SUM(tokens_out),0),COALESCE(SUM(sandbox_seconds),0),COALESCE(SUM(cpu_seconds),0) FROM usage_daily WHERE user_id=? AND day LIKE ?`, userID, dayPrefix+"%").
		Scan(&u.TokensIn, &u.TokensOut, &u.SandboxSeconds, &u.CPUSeconds)
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

func levelOrUser(l string) string {
	switch l {
	case LevelPowerUser, LevelAdmin:
		return l
	}
	return LevelUser
}

// ---- levels and groups ----

func scanPolicy(row interface{ Scan(...any) error }) (*Policy, error) {
	var p Policy
	var data string
	if err := row.Scan(&p.ID, &p.Kind, &p.Name, &data); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(data), &p.Data); err != nil {
		p.Data = PolicyData{}
	}
	return &p, nil
}

// ListPolicies returns the levels or groups, ordered by name.
func (s *SQLite) ListPolicies(ctx context.Context, kind string) ([]Policy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,name,policy FROM policies WHERE kind=? ORDER BY name`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPolicy returns one level or group by name.
func (s *SQLite) GetPolicy(ctx context.Context, kind, name string) (*Policy, error) {
	return scanPolicy(s.db.QueryRowContext(ctx, `SELECT id,kind,name,policy FROM policies WHERE kind=? AND name=?`, kind, name))
}

// GetPolicyByID returns one level or group by id.
func (s *SQLite) GetPolicyByID(ctx context.Context, id int64) (*Policy, error) {
	return scanPolicy(s.db.QueryRowContext(ctx, `SELECT id,kind,name,policy FROM policies WHERE id=?`, id))
}

// UpsertPolicy creates or updates a level or group.
func (s *SQLite) UpsertPolicy(ctx context.Context, p Policy) (int64, error) {
	data, _ := json.Marshal(p.Data)
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO policies (kind,name,policy) VALUES (?,?,?) ON CONFLICT(kind,name) DO UPDATE SET policy=excluded.policy`, p.Kind, p.Name, string(data))
		if err != nil {
			return 0, mapErr(err)
		}
		if id, err := res.LastInsertId(); err == nil && id > 0 {
			if got, err := s.GetPolicy(ctx, p.Kind, p.Name); err == nil {
				return got.ID, nil
			}
			return id, nil
		}
		got, err := s.GetPolicy(ctx, p.Kind, p.Name)
		if err != nil {
			return 0, err
		}
		return got.ID, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE policies SET name=?,policy=? WHERE id=?`, p.Name, string(data), p.ID)
	return p.ID, affected(res, err)
}

// DeletePolicy removes a level or group; users of a deleted group fall back to no group.
func (s *SQLite) DeletePolicy(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET group_id=0 WHERE group_id=?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM policies WHERE id=?`, id)
	return affected(res, err)
}

// ---- connections ----

// ListConnections returns a user's connections.
func (s *SQLite) ListConnections(ctx context.Context, userID int64) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,account,detail,settings,checked_at FROM connections WHERE user_id=? ORDER BY kind`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c := Connection{UserID: userID}
		var settings string
		var checked int64
		if err := rows.Scan(&c.Kind, &c.Account, &c.Detail, &settings, &checked); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(settings), &c.Settings); err != nil {
			c.Settings = nil
		}
		c.CheckedAt = fromTS(checked)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutConnection creates or replaces a connection record.
func (s *SQLite) PutConnection(ctx context.Context, c Connection) error {
	settings, _ := json.Marshal(c.Settings)
	if c.Settings == nil {
		settings = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (user_id,kind,account,detail,settings,checked_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(user_id,kind) DO UPDATE SET account=excluded.account,detail=excluded.detail,settings=excluded.settings,checked_at=excluded.checked_at`,
		c.UserID, c.Kind, c.Account, c.Detail, string(settings), ts(c.CheckedAt))
	return err
}

// DeleteConnection removes a connection record.
func (s *SQLite) DeleteConnection(ctx context.Context, userID int64, kind string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE user_id=? AND kind=?`, userID, kind)
	return err
}

// ---- automations ----

const autoCols = `id,user_id,name,enabled,trigger,config,prompt,model,autonomy,repo,notify,token_hash,state,next_run_at,last_run_at,last_status,last_error,fail_count,created_at`

func scanAutomation(row interface{ Scan(...any) error }) (*Automation, error) {
	var a Automation
	var en int
	var cfg, notify string
	var next, last, created int64
	if err := row.Scan(&a.ID, &a.UserID, &a.Name, &en, &a.Trigger, &cfg, &a.Prompt, &a.Model, &a.Autonomy, &a.Repo, &notify, &a.TokenHash, &a.State, &next, &last, &a.LastStatus, &a.LastError, &a.FailCount, &created); err != nil {
		return nil, mapErr(err)
	}
	a.Enabled = en == 1
	_ = json.Unmarshal([]byte(cfg), &a.Config)
	_ = json.Unmarshal([]byte(notify), &a.Notify)
	a.NextRunAt, a.LastRunAt, a.CreatedAt = fromTS(next), fromTS(last), fromTS(created)
	return &a, nil
}

func mapJSON(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// CreateAutomation inserts an automation.
func (s *SQLite) CreateAutomation(ctx context.Context, a Automation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO automations (`+autoCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.UserID, a.Name, b2i(a.Enabled), a.Trigger, mapJSON(a.Config), a.Prompt, a.Model, a.Autonomy, a.Repo, mapJSON(a.Notify), a.TokenHash, a.State,
		ts(a.NextRunAt), ts(a.LastRunAt), a.LastStatus, a.LastError, a.FailCount, ts(a.CreatedAt))
	return mapErr(err)
}

// UpdateAutomation replaces an automation's fields.
func (s *SQLite) UpdateAutomation(ctx context.Context, a Automation) error {
	res, err := s.db.ExecContext(ctx, `UPDATE automations SET name=?,enabled=?,trigger=?,config=?,prompt=?,model=?,autonomy=?,repo=?,notify=?,token_hash=?,state=?,next_run_at=?,last_run_at=?,last_status=?,last_error=?,fail_count=? WHERE id=?`,
		a.Name, b2i(a.Enabled), a.Trigger, mapJSON(a.Config), a.Prompt, a.Model, a.Autonomy, a.Repo, mapJSON(a.Notify), a.TokenHash, a.State,
		ts(a.NextRunAt), ts(a.LastRunAt), a.LastStatus, a.LastError, a.FailCount, a.ID)
	return affected(res, err)
}

// GetAutomation fetches one automation.
func (s *SQLite) GetAutomation(ctx context.Context, id string) (*Automation, error) {
	return scanAutomation(s.db.QueryRowContext(ctx, `SELECT `+autoCols+` FROM automations WHERE id=?`, id))
}

func (s *SQLite) queryAutomations(ctx context.Context, q string, args ...any) ([]Automation, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Automation
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListAutomations returns a user's automations.
func (s *SQLite) ListAutomations(ctx context.Context, userID int64) ([]Automation, error) {
	return s.queryAutomations(ctx, `SELECT `+autoCols+` FROM automations WHERE user_id=? ORDER BY created_at`, userID)
}

// DueAutomations returns enabled automations whose next run time has come.
func (s *SQLite) DueAutomations(ctx context.Context, now time.Time, limit int) ([]Automation, error) {
	return s.queryAutomations(ctx, `SELECT `+autoCols+` FROM automations WHERE enabled=1 AND next_run_at>0 AND next_run_at<=? ORDER BY next_run_at LIMIT ?`, ts(now), limit)
}

// DeleteAutomation removes an automation and its run history.
func (s *SQLite) DeleteAutomation(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM automation_runs WHERE automation_id=?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM automations WHERE id=?`, id)
	return affected(res, err)
}

// AddAutomationRun records a start (and keeps only the latest 50 per automation).
func (s *SQLite) AddAutomationRun(ctx context.Context, r AutomationRun) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO automation_runs (automation_id,session_id,started_at,source,detail) VALUES (?,?,?,?,?)`, r.AutomationID, r.SessionID, ts(r.StartedAt), r.Source, r.Detail); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM automation_runs WHERE automation_id=? AND id NOT IN (SELECT id FROM automation_runs WHERE automation_id=? ORDER BY id DESC LIMIT 50)`, r.AutomationID, r.AutomationID)
	return err
}

// ListAutomationRuns returns the latest runs, newest first.
func (s *SQLite) ListAutomationRuns(ctx context.Context, automationID string, limit int) ([]AutomationRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,automation_id,session_id,started_at,source,detail FROM automation_runs WHERE automation_id=? ORDER BY id DESC LIMIT ?`, automationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutomationRun
	for rows.Next() {
		var r AutomationRun
		var at int64
		if err := rows.Scan(&r.ID, &r.AutomationID, &r.SessionID, &at, &r.Source, &r.Detail); err != nil {
			return nil, err
		}
		r.StartedAt = fromTS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountAutomationRunsSince counts starts since a time (for the daily cap).
func (s *SQLite) CountAutomationRunsSince(ctx context.Context, automationID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_runs WHERE automation_id=? AND started_at>=?`, automationID, ts(since)).Scan(&n)
	return n, err
}

// ---- shared session links ----

const shareCols = `id,session_id,user_id,name,token_hash,token_enc,include_tools,created_at,expires_at,revoked,views,last_view_at`

func scanShare(row interface{ Scan(...any) error }) (*Share, error) {
	var s Share
	var inc, rev int
	var cr, ex, lv int64
	if err := row.Scan(&s.ID, &s.SessionID, &s.UserID, &s.Name, &s.TokenHash, &s.TokenEnc, &inc, &cr, &ex, &rev, &s.Views, &lv); err != nil {
		return nil, mapErr(err)
	}
	s.IncludeTools, s.Revoked = inc == 1, rev == 1
	s.CreatedAt, s.ExpiresAt, s.LastViewAt = fromTS(cr), fromTS(ex), fromTS(lv)
	return &s, nil
}

// CreateShare stores a share.
func (s *SQLite) CreateShare(ctx context.Context, sh Share) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO shares (`+shareCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		sh.ID, sh.SessionID, sh.UserID, sh.Name, sh.TokenHash, sh.TokenEnc, b2i(sh.IncludeTools), ts(sh.CreatedAt), ts(sh.ExpiresAt), b2i(sh.Revoked), sh.Views, ts(sh.LastViewAt))
	return mapErr(err)
}

// GetShareByHash looks a share up by the hash of the secret in its link.
func (s *SQLite) GetShareByHash(ctx context.Context, hash string) (*Share, error) {
	return scanShare(s.db.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE token_hash=?`, hash))
}

// GetShare fetches one share by id.
func (s *SQLite) GetShare(ctx context.Context, id string) (*Share, error) {
	return scanShare(s.db.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE id=?`, id))
}

// ListShares returns a user's shares, newest first.
func (s *SQLite) ListShares(ctx context.Context, userID int64) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+shareCols+` FROM shares WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

// RevokeShare switches a link off.
func (s *SQLite) RevokeShare(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE shares SET revoked=1 WHERE id=?`, id)
	return affected(res, err)
}

// RecordShareView counts a view.
func (s *SQLite) RecordShareView(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET views=views+1,last_view_at=? WHERE id=?`, ts(at), id)
	return err
}

// CountActiveShares counts a user's links that are neither revoked nor expired.
func (s *SQLite) CountActiveShares(ctx context.Context, userID int64, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shares WHERE user_id=? AND revoked=0 AND expires_at>?`, userID, ts(now)).Scan(&n)
	return n, err
}

// ---- checkpoint marks ----

// PutCheckpointMark records where in a conversation a checkpoint was taken.
func (s *SQLite) PutCheckpointMark(ctx context.Context, m CheckpointMark) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO checkpoint_marks (user_id,seq,session_id,message_id,label,created_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(user_id,seq) DO UPDATE SET session_id=excluded.session_id,message_id=excluded.message_id,label=excluded.label,created_at=excluded.created_at`,
		m.UserID, m.Seq, m.SessionID, m.MessageID, m.Label, ts(m.CreatedAt))
	return err
}

// GetCheckpointMark fetches one mark.
func (s *SQLite) GetCheckpointMark(ctx context.Context, userID int64, seq int) (*CheckpointMark, error) {
	var m CheckpointMark
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id,seq,session_id,message_id,label,created_at FROM checkpoint_marks WHERE user_id=? AND seq=?`, userID, seq).
		Scan(&m.UserID, &m.Seq, &m.SessionID, &m.MessageID, &m.Label, &at)
	if err != nil {
		return nil, mapErr(err)
	}
	m.CreatedAt = fromTS(at)
	return &m, nil
}

// ListCheckpointMarks returns a user's marks.
func (s *SQLite) ListCheckpointMarks(ctx context.Context, userID int64) ([]CheckpointMark, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,seq,session_id,message_id,label,created_at FROM checkpoint_marks WHERE user_id=? ORDER BY seq`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckpointMark
	for rows.Next() {
		var m CheckpointMark
		var at int64
		if err := rows.Scan(&m.UserID, &m.Seq, &m.SessionID, &m.MessageID, &m.Label, &at); err != nil {
			return nil, err
		}
		m.CreatedAt = fromTS(at)
		out = append(out, m)
	}
	return out, rows.Err()
}
