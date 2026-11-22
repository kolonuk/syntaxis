-- +goose Up
-- Personal API tokens: a user creates them in Settings to let a script or tool act as them with a
-- limited scope. Only a hash is stored; the token itself is shown once.
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL,
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  scope        TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);

-- +goose Down
DROP TABLE api_tokens;
