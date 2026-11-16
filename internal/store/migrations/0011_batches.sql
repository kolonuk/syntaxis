-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN batch_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_sessions_batch ON chat_sessions(batch_id);

-- +goose Down
DROP INDEX idx_sessions_batch;
ALTER TABLE chat_sessions DROP COLUMN batch_id;
