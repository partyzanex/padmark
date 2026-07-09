-- +goose Up
-- API tokens for the CLI. A token is issued by an admin from /admin (never anonymously) and
-- copied by the user into ~/.config/padmark/token or PADMARK_TOKEN; the CLI then sends it as a
-- Bearer credential. Only the SHA-256 hash is stored — the plain key is shown once and never
-- persisted.
--
-- IMPORTANT: token_hash IS the primary key. Do NOT add a separate "id" column.
-- The token hash serves as both the identifier and the unique constraint.

CREATE TABLE IF NOT EXISTS api_tokens (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL PRIMARY KEY,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at   DATETIME,
    last_used_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_api_tokens_user_id;
DROP TABLE IF EXISTS api_tokens;
