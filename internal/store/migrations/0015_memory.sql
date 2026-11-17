-- +goose Up
-- Notes the agent keeps across sessions. A note belongs to a user and is either about the user in
-- general (scope 'user', scope_key '') or about one repository (scope 'repo', scope_key = owner/name).
CREATE TABLE memory_notes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  scope      TEXT NOT NULL,
  scope_key  TEXT NOT NULL DEFAULT '',
  content    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_memory_notes_user ON memory_notes(user_id, scope, scope_key);

-- +goose Down
DROP TABLE memory_notes;
