-- +goose Up
CREATE TABLE shares (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  token_enc BLOB NOT NULL,            -- the secret in the link, sealed with the master key so the owner can copy the link again
  include_tools INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0,
  views INTEGER NOT NULL DEFAULT 0,
  last_view_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_shares_user ON shares(user_id);
CREATE INDEX idx_shares_session ON shares(session_id);

-- +goose Down
DROP TABLE shares;
