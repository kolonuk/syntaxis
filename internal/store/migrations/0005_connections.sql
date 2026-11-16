-- +goose Up
CREATE TABLE connections (
  user_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  account TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  settings TEXT NOT NULL DEFAULT '{}',  -- the non-secret fields (host, region, ...); credentials live in the secrets vault
  checked_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, kind)
);

-- +goose Down
DROP TABLE connections;
