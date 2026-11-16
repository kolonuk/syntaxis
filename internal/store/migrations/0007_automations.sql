-- +goose Up
CREATE TABLE automations (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  trigger TEXT NOT NULL,                 -- schedule | webhook | watch
  config TEXT NOT NULL DEFAULT '{}',     -- JSON object of strings: cron, tz | kind and its fields, interval_min
  prompt TEXT NOT NULL,
  model TEXT NOT NULL,
  autonomy TEXT NOT NULL DEFAULT 'workspace',
  repo TEXT NOT NULL DEFAULT '',         -- JSON repository spec, or empty
  notify TEXT NOT NULL DEFAULT '{}',     -- JSON object of strings (ifttt_event)
  token_hash TEXT NOT NULL DEFAULT '',   -- webhook trigger: hash of the secret in the address
  state TEXT NOT NULL DEFAULT '',        -- what a watch remembers between checks
  next_run_at INTEGER NOT NULL DEFAULT 0,
  last_run_at INTEGER NOT NULL DEFAULT 0,
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  fail_count INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_automations_user ON automations(user_id);
CREATE INDEX idx_automations_due ON automations(enabled, next_run_at);
CREATE TABLE automation_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  automation_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_automation_runs ON automation_runs(automation_id, id);

-- +goose Down
DROP TABLE automation_runs;
DROP TABLE automations;
