-- +goose Up
-- A provider belongs to the admin (owner_id 0: local servers and shared services) or to one user
-- (owner_id = that user: a hosted service the user brought with their own key). A user's provider
-- has a unique internal name ("@<user id>:<display name>"); display_name is what the user typed.
ALTER TABLE providers ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE providers ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
CREATE INDEX providers_owner ON providers (owner_id);

-- +goose Down
DROP INDEX providers_owner;
ALTER TABLE providers DROP COLUMN display_name;
ALTER TABLE providers DROP COLUMN owner_id;
