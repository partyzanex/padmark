-- +goose Up
ALTER TABLE notes ADD COLUMN edit_code TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE notes DROP COLUMN edit_code;
