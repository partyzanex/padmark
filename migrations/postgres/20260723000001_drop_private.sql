-- +goose Up
-- private was superseded by the privacy enum (see 20260720000001_notes_privacy.sql) and has not
-- been read or written by the app since. Dropping it is not reversible: Down below restores the
-- column's structure (for goose round-trip consistency) but not its data — every row's original
-- private value is gone once this Up runs.
ALTER TABLE notes DROP COLUMN private;

-- +goose Down
ALTER TABLE notes ADD COLUMN private BOOLEAN NOT NULL DEFAULT FALSE;
