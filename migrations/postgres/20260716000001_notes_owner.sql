-- +goose Up
-- Lets a note record who created it while authenticated (session or API token), so
-- notes.Manager can let that same user edit/delete without the edit_code. NULL means the note
-- was created anonymously (or accounts are disabled) — those notes keep requiring edit_code from
-- everyone, exactly as before this migration.
ALTER TABLE notes ADD COLUMN owner_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE notes DROP COLUMN owner_id;
