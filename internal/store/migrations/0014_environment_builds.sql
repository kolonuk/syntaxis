-- +goose Up
-- An environment built from the catalog by Syntaxis (managed) can be removed again when it has not
-- been used for a while; last_used is when a session last started in it.
ALTER TABLE image_templates ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE image_templates ADD COLUMN last_used INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE image_templates DROP COLUMN last_used;
ALTER TABLE image_templates DROP COLUMN managed;
