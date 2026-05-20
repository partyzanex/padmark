-- +goose Up
-- Notes created before this migration have plaintext slugs as PKs.
-- They are unreachable via the new hash-based lookup and cannot be decrypted
-- without the plaintext slug. Expire them so they are cleaned up on next access.
UPDATE notes SET expires_at = NOW()
WHERE expires_at IS NULL OR expires_at > NOW();

-- Reveal tokens reference note IDs that are now hashed; old tokens are unusable.
DELETE FROM reveal_tokens WHERE used_at IS NULL;

-- +goose Down
-- No-op: cannot recover plaintext slugs from hashes.
