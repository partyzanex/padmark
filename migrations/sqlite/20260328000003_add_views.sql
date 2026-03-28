-- +goose Up
ALTER TABLE notes ADD COLUMN views INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE notes DROP COLUMN views;
