-- +goose Up
CREATE TABLE IF NOT EXISTS notes (
    id           TEXT     NOT NULL PRIMARY KEY,
    title        TEXT     NOT NULL,
    content      TEXT     NOT NULL DEFAULT '',
    content_type TEXT     NOT NULL DEFAULT 'text/markdown',
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS notes;
