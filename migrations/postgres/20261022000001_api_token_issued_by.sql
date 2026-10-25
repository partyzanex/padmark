-- +goose Up
-- issued_by records which API token's bearer issued a token (NULL = issued from a browser
-- session). ON DELETE CASCADE makes revocation follow it: revoking a leaked key also revokes
-- every key minted with it, and those keys' own descendants.
ALTER TABLE api_tokens ADD COLUMN issued_by TEXT REFERENCES api_tokens(token_hash) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_api_tokens_issued_by ON api_tokens(issued_by);

-- +goose Down
-- Tokens keep working; they just stop being linked to their issuer.
DROP INDEX IF EXISTS idx_api_tokens_issued_by;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS issued_by;
