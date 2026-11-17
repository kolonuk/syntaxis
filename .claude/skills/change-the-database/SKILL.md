---
name: change-the-database
description: Use when a change needs a new table or column in the SQLite store. Shows the migration, store interface and scan steps that must all agree.
---

# Changing the database

The store is SQLite through `internal/store` with goose migrations in `internal/store/migrations`.

1. Add the next numbered migration (`00NN_name.sql`) with `-- +goose Up` and a working `-- +goose Down`. Give new columns a default so existing rows stay valid.
2. Add the field to the struct in `store.go`. For a new query add the method to the `Store` interface.
3. Update every place that lists the columns in `sqlite.go`: the insert, the column constant, and the scan (for sessions: `CreateChatSession`, `sessCols`, `scanSession`). A missed one fails at runtime, not at compile time.
4. Keep an `UpdateX` in step if the new field changes after creation.
5. Test with the real store (`store.OpenSQLite` on a temp file). In server tests that start sandboxes use `SetUserIDFloor` so user ids do not collide with real volumes.
6. Anything stored per user must be read only through that user's id; check the handler refuses other users with a 404.
