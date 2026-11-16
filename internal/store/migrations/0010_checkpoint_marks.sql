-- +goose Up
CREATE TABLE checkpoint_marks (
  user_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  message_id INTEGER NOT NULL,     -- the last message of the session when the snapshot was taken
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, seq)
);

-- +goose Down
DROP TABLE checkpoint_marks;
