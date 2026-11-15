-- +goose Up
-- show_contents turns on the "On this page" sidebar in the note view. The default keeps the
-- full-width view for all notes that exist before this migration.
ALTER TABLE notes ADD COLUMN show_contents BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE notes DROP COLUMN show_contents;
