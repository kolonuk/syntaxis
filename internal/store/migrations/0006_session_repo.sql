-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN repo_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN repo_name TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN repo_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN repo_base TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN repo_base;
ALTER TABLE chat_sessions DROP COLUMN repo_branch;
ALTER TABLE chat_sessions DROP COLUMN repo_url;
ALTER TABLE chat_sessions DROP COLUMN repo_name;
ALTER TABLE chat_sessions DROP COLUMN repo_kind;
