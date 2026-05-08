-- +goose Up
CREATE INDEX idx_notes_expires_at ON notes (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_notes_expires_at;
