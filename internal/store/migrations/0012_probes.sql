-- +goose Up
-- What a live probe of a model server found: whether a model really calls tools, and whether a
-- provider's stream arrives live. One row per provider, model and kind; the newest wins.
CREATE TABLE probes (
  provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  model       TEXT    NOT NULL DEFAULT '',
  kind        TEXT    NOT NULL,
  result      INTEGER NOT NULL,
  detail      TEXT    NOT NULL DEFAULT '',
  at          INTEGER NOT NULL,
  PRIMARY KEY (provider_id, model, kind)
);

-- +goose Down
DROP TABLE probes;
