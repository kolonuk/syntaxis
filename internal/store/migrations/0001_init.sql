-- +goose Up
CREATE TABLE admins (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  totp_secret BLOB,
  totp_enabled INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  password_changed_at INTEGER NOT NULL
);

CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  password_hash TEXT NOT NULL DEFAULT '',
  totp_secret BLOB,
  totp_enabled INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  password_changed_at INTEGER NOT NULL DEFAULT 0,
  remind_2fa_after INTEGER NOT NULL DEFAULT 0,
  quota_daily_tokens INTEGER NOT NULL DEFAULT 0,
  quota_monthly_tokens INTEGER NOT NULL DEFAULT 0,
  max_sessions INTEGER NOT NULL DEFAULT 0,
  egress_mode TEXT NOT NULL DEFAULT '',
  allowed_models TEXT NOT NULL DEFAULT '[]',
  cloud_tools_disabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE recovery_codes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_kind TEXT NOT NULL,
  owner_id INTEGER NOT NULL,
  code_hash TEXT NOT NULL,
  used_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_recovery_owner ON recovery_codes(owner_kind, owner_id);

CREATE TABLE tokens (
  hash TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE auth_sessions (
  id_hash TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  owner_id INTEGER NOT NULL,
  csrf TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  mfa_ok INTEGER NOT NULL DEFAULT 0,
  pending_mfa INTEGER NOT NULL DEFAULT 0,
  ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_auth_sessions_owner ON auth_sessions(kind, owner_id);

CREATE TABLE login_attempts (
  key TEXT PRIMARY KEY,
  failures INTEGER NOT NULL,
  window_start INTEGER NOT NULL,
  locked_until INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE providers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  base_url TEXT NOT NULL,
  api_key_enc BLOB,
  concurrency INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  context_override INTEGER NOT NULL DEFAULT 0,
  digest TEXT NOT NULL DEFAULT '',
  tools_supported INTEGER NOT NULL DEFAULT -1,
  recommended INTEGER NOT NULL DEFAULT 0,
  seen_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(provider_id, name)
);

CREATE TABLE chat_sessions (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  title TEXT NOT NULL,
  provider_id INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL,
  autonomy TEXT NOT NULL DEFAULT 'workspace',
  state TEXT NOT NULL DEFAULT 'idle',
  sandbox_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_chat_sessions_user ON chat_sessions(user_id);

CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
  branch INTEGER NOT NULL DEFAULT 0,
  abandoned INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_calls TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_messages_session ON messages(session_id, id);

CREATE TABLE usage_daily (
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  sandbox_seconds INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day)
);

CREATE TABLE secrets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  owner_id INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL,
  ciphertext BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(scope, owner_id, name)
);

CREATE TABLE audit_log (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  time INTEGER NOT NULL,
  time_nanos INTEGER NOT NULL DEFAULT 0,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  detail TEXT NOT NULL,
  prev_hash TEXT NOT NULL,
  hash TEXT NOT NULL
);

CREATE TABLE egress_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  time INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  dest_host TEXT NOT NULL,
  dest_ip TEXT NOT NULL,
  port INTEGER NOT NULL,
  protocol TEXT NOT NULL,
  bytes INTEGER NOT NULL DEFAULT 0,
  allowed INTEGER NOT NULL,
  dns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_egress_user ON egress_log(user_id, time);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE benchmarks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model_id INTEGER NOT NULL,
  time INTEGER NOT NULL,
  digest TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL
);

CREATE TABLE push_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL
);

-- +goose Down
DROP TABLE push_subscriptions;
DROP TABLE benchmarks;
DROP TABLE settings;
DROP TABLE egress_log;
DROP TABLE audit_log;
DROP TABLE secrets;
DROP TABLE usage_daily;
DROP TABLE messages;
DROP TABLE chat_sessions;
DROP TABLE models;
DROP TABLE providers;
DROP TABLE login_attempts;
DROP TABLE auth_sessions;
DROP TABLE tokens;
DROP TABLE recovery_codes;
DROP TABLE users;
DROP TABLE admins;
