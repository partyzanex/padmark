-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id            UUID        NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT        NOT NULL UNIQUE,
    totp_secret   TEXT        NOT NULL,
    is_admin      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

CREATE TABLE IF NOT EXISTS invites (
    token      TEXT        NOT NULL PRIMARY KEY,
    created_by UUID        NOT NULL REFERENCES users(id),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    used_by    TEXT
);

CREATE INDEX IF NOT EXISTS idx_invites_active ON invites(expires_at) WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT        NOT NULL PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    user_agent TEXT        NOT NULL DEFAULT '',
    ip         TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id  ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_sessions_expires_at;
DROP INDEX IF EXISTS idx_sessions_user_id;
DROP TABLE IF EXISTS sessions;
DROP INDEX IF EXISTS idx_invites_active;
DROP TABLE IF EXISTS invites;
DROP INDEX IF EXISTS idx_users_username;
DROP TABLE IF EXISTS users;
