-- +goose Up
-- Mark all existing notes as expired so that plaintext (pre-encryption) content
-- is cleaned up on next access rather than returning ErrDecryptionFailed.
UPDATE notes SET expires_at = NOW() WHERE expires_at IS NULL OR expires_at > NOW();

-- +goose Down
-- No-op: cannot distinguish notes expired by this migration from notes that expired naturally.
