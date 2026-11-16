-- +goose Up
ALTER TABLE users ADD COLUMN level TEXT NOT NULL DEFAULT 'user';
ALTER TABLE users ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN quota_cpu_minutes INTEGER NOT NULL DEFAULT 0;
CREATE TABLE policies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,            -- 'level' or 'group'
  name TEXT NOT NULL,
  policy TEXT NOT NULL DEFAULT '{}',
  UNIQUE(kind, name)
);

-- +goose Down
DROP TABLE policies;
ALTER TABLE users DROP COLUMN quota_cpu_minutes;
ALTER TABLE users DROP COLUMN group_id;
ALTER TABLE users DROP COLUMN level;
