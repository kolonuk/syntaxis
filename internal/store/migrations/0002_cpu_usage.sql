-- +goose Up
ALTER TABLE usage_daily ADD COLUMN cpu_seconds INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE usage_daily DROP COLUMN cpu_seconds;
