-- +goose Up
-- issued_by records which API token's bearer issued a token (NULL = issued from a browser
-- session). ON DELETE CASCADE makes revocation follow it: revoking a leaked key also revokes
-- every key minted with it, and those keys' own descendants.
ALTER TABLE api_tokens ADD COLUMN issued_by TEXT REFERENCES api_tokens(token_hash) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_api_tokens_issued_by ON api_tokens(issued_by);

-- +goose Down
-- SQLite cannot drop a column that takes part in a foreign key, so the table is rebuilt in its
-- previous shape. Tokens keep working; they just stop being linked to their issuer.
DROP INDEX IF EXISTS idx_api_tokens_issued_by;
CREATE TABLE api_tokens_without_issuer (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL PRIMARY KEY,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at   DATETIME,
    last_used_at DATETIME
);
INSERT INTO api_tokens_without_issuer (user_id, token_hash, created_at, expires_at, last_used_at)
    SELECT user_id, token_hash, created_at, expires_at, last_used_at FROM api_tokens;
-- Unlink before dropping: the drop deletes rows, which would otherwise cascade along issued_by.
UPDATE api_tokens SET issued_by = NULL;
DROP TABLE api_tokens;
ALTER TABLE api_tokens_without_issuer RENAME TO api_tokens;
CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
