-- +goose Up
-- Push notifications were never wired up and have been removed, with the VAPID keys. The table
-- held browser push subscriptions and nothing read or wrote it.
DROP TABLE IF EXISTS push_subscriptions;

-- +goose Down
CREATE TABLE push_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL
);
