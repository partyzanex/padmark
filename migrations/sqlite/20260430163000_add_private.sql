-- +goose Up
ALTER TABLE notes ADD COLUMN private INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE notes DROP COLUMN private;
