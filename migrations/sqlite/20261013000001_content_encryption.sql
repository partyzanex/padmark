-- +goose Up
CREATE TABLE content_encryption_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    installation_id TEXT NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('pending', 'ready'))
);
CREATE TABLE content_encryption_keys (
    key_id TEXT PRIMARY KEY,
    verification TEXT NOT NULL
);

-- No automatic down migration: deleting the registry would permit accidental key rebinding.
