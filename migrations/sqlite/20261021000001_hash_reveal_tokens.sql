-- +goose Up
-- Reveal tokens are bearer credentials for burn-after-reading notes. From now on only their
-- base64url SHA-256 is stored, like sessions, invites and API tokens.
-- The raw values of existing rows cannot be recovered or hashed in SQL (SQLite has no SHA-256
-- without an extension), so outstanding tokens are invalidated. They live 10 minutes at most:
-- a reader mid-flow simply reloads the note to get a fresh confirmation page. The notes
-- themselves are untouched.
ALTER TABLE reveal_tokens RENAME COLUMN token TO token_hash;
DELETE FROM reveal_tokens;

-- +goose Down
-- Raw tokens cannot be recovered. Drop hashed-era rows too, so an older binary cannot treat a
-- stored digest as a bearer token after rollback.
DELETE FROM reveal_tokens;
ALTER TABLE reveal_tokens RENAME COLUMN token_hash TO token;
