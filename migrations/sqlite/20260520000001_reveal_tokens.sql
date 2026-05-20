-- +goose Up
CREATE TABLE IF NOT EXISTS reveal_tokens (
    token      TEXT     NOT NULL PRIMARY KEY,
    note_id    TEXT     NOT NULL,
    expires_at DATETIME NOT NULL,
    used_at    DATETIME
);

-- Partial index: active tokens (not yet used) ordered by expiry — used for cleanup queries
CREATE INDEX IF NOT EXISTS idx_reveal_tokens_active ON reveal_tokens(expires_at) WHERE used_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_reveal_tokens_active;
DROP TABLE IF EXISTS reveal_tokens;
