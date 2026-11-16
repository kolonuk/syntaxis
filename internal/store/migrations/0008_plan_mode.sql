-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN plan_mode INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chat_sessions ADD COLUMN plan_state TEXT NOT NULL DEFAULT '';   -- '' | proposed | approved
ALTER TABLE chat_sessions ADD COLUMN plan_text TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN plan_text;
ALTER TABLE chat_sessions DROP COLUMN plan_state;
ALTER TABLE chat_sessions DROP COLUMN plan_mode;
