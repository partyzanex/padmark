-- +goose Up
-- Username uniqueness already has its own index. Notes expiry is checked only
-- after primary-key lookup; there is no table-wide notes expiry sweep.
DROP INDEX IF EXISTS idx_users_username;
DROP INDEX IF EXISTS idx_notes_expires_at;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_notes_expires_at ON notes(expires_at) WHERE expires_at IS NOT NULL;
