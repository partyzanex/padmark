-- +goose Up
ALTER TABLE notes ADD COLUMN burn_after_reading INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE notes DROP COLUMN burn_after_reading;
