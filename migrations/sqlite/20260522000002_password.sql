-- +goose Up
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN kdf_salt      TEXT NOT NULL DEFAULT '';

-- All existing users have TOTP secrets encrypted with user UUID (C2 vulnerability).
-- Re-registration is required; expire all existing users, sessions, and invites.
DELETE FROM sessions;
DELETE FROM invites;
DELETE FROM users;

-- +goose Down
ALTER TABLE users DROP COLUMN kdf_salt;
ALTER TABLE users DROP COLUMN password_hash;
