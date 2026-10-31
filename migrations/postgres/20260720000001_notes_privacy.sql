-- +goose Up
-- private is deprecated in favor of privacy; kept for now, no longer read or written by the app.
ALTER TABLE notes ADD COLUMN privacy TEXT NOT NULL DEFAULT 'public';
UPDATE notes SET privacy = 'authenticated' WHERE private = TRUE;

-- +goose Down
ALTER TABLE notes DROP COLUMN privacy;
