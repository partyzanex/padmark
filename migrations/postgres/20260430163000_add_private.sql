-- +goose Up
ALTER TABLE notes ADD COLUMN private BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE notes DROP COLUMN private;
