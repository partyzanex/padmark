-- +goose Up
-- API tokens issued through the browser-based CLI login flow.
-- A token is created only when an authenticated browser session confirms the request
-- on /login/cli. The link shown by the CLI is a signed, time-limited URL and does not
-- require any unauthenticated write to the database.
--
-- IMPORTANT: token_hash IS the primary key. Do NOT add a separate "id" column.
-- The token hash serves as both the identifier and the unique constraint.

CREATE TABLE IF NOT EXISTS api_tokens (
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL PRIMARY KEY,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS api_tokens;
