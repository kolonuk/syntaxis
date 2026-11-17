-- +goose Up
-- Environments an admin has approved for sandboxes: a prepared image, and the images a repository's
-- devcontainer.json may name to get it. A session records the image it runs in.
CREATE TABLE image_templates (
  name        TEXT PRIMARY KEY,
  image       TEXT NOT NULL,
  matches     TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT ''
);
ALTER TABLE chat_sessions ADD COLUMN image TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN env_note TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN env_note;
ALTER TABLE chat_sessions DROP COLUMN image;
DROP TABLE image_templates;
