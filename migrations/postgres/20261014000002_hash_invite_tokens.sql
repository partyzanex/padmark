-- +goose Up
-- Invite tokens are bearer credentials. Invalidate plaintext-era invitations,
-- matching the session migration; administrators must issue new invitation links.
-- Use the same upgrade policy as SQLite and persist only base64url SHA-256.
ALTER TABLE invites RENAME COLUMN token TO token_hash;
DELETE FROM invites;

-- +goose Down
-- Raw tokens cannot be recovered. Invalidate hashed-era invitations as well,
-- so older binaries cannot treat stored hashes as bearer tokens after rollback.
DELETE FROM invites;
ALTER TABLE invites RENAME COLUMN token_hash TO token;
