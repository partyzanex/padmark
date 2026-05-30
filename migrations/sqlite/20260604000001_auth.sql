-- +goose Up
-- Consolidated auth/security migration for the feature/security-improvements branch.
-- Replaces the branch's separate auth + hash_slugs + password migrations into one.
-- Pre-branch migrations are left untouched (already applied in production).
--
-- Two security fixes are folded into the schema:
--   * sessions/invites FKs use ON DELETE CASCADE so revoking a user clears them
--     (effective only when the connection sets PRAGMA foreign_keys=ON — see openSQLiteDB).
--   * a partial unique index on users.is_admin guards the first-admin bootstrap
--     race (admins are only ever created via AcceptFirstAdmin; invites produce
--     non-admins).
--
-- The password migration's DELETE of existing users/sessions/invites is dropped:
-- those tables are created fresh here, so there is nothing to wipe.

-- Legacy plaintext-slug notes are unreachable after the move to hashed slugs and
-- cannot be decrypted; expire them so they are cleaned up on next access.
UPDATE notes SET expires_at = CURRENT_TIMESTAMP
WHERE expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP;

-- Reveal tokens reference note IDs that are now hashed; old tokens are unusable.
DELETE FROM reveal_tokens WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS users (
    id            TEXT     NOT NULL PRIMARY KEY,
    username      TEXT     NOT NULL UNIQUE,
    totp_secret   TEXT     NOT NULL,
    password_hash TEXT     NOT NULL DEFAULT '',
    kdf_salt      TEXT     NOT NULL DEFAULT '',
    is_admin          INTEGER  NOT NULL DEFAULT 0,
    last_totp_counter INTEGER  NOT NULL DEFAULT 0,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now')),
    last_login_at     DATETIME
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

-- At most one admin row: atomically guards the first-admin bootstrap race.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_single_admin ON users (is_admin) WHERE is_admin = 1;

CREATE TABLE IF NOT EXISTS invites (
    token      TEXT     NOT NULL PRIMARY KEY,
    created_by TEXT     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    used_at    DATETIME,
    used_by    TEXT
);

CREATE INDEX IF NOT EXISTS idx_invites_active ON invites(expires_at) WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT     NOT NULL PRIMARY KEY,
    user_id    TEXT     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL,
    user_agent TEXT     NOT NULL DEFAULT '',
    ip         TEXT     NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id   ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_sessions_expires_at;
DROP INDEX IF EXISTS idx_sessions_user_id;
DROP TABLE IF EXISTS sessions;
DROP INDEX IF EXISTS idx_invites_active;
DROP TABLE IF EXISTS invites;
DROP INDEX IF EXISTS idx_users_single_admin;
DROP INDEX IF EXISTS idx_users_username;
DROP TABLE IF EXISTS users;
-- The notes/reveal_tokens data changes are irreversible (no-op down).
