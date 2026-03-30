-- +goose Up
CREATE TABLE IF NOT EXISTS notes (
    id                 TEXT        NOT NULL PRIMARY KEY,
    title              TEXT        NOT NULL,
    content            TEXT        NOT NULL DEFAULT '',
    content_type       TEXT        NOT NULL DEFAULT 'text/markdown',
    edit_code          TEXT        NOT NULL DEFAULT '',
    views              INTEGER     NOT NULL DEFAULT 0,
    burn_after_reading BOOLEAN     NOT NULL DEFAULT FALSE,
    burn_ttl           BIGINT      NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS notes;
