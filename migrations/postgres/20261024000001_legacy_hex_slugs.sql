-- +goose Up
-- Tag the v0.1.6 notes that 20261023000001_legacy_notes cannot tell apart by id: a custom slug
-- that is itself 64 lowercase hex characters. Such a row looks like a hashed id, but its
-- edit_code is still the plaintext code. Every release after v0.1.6 stores only hashed edit
-- codes ("v1$…" argon2id or "s1$…" salted SHA-256), and encryption migrate writes a hash when
-- it converts a row, so an untagged row without a hash prefix can only be a v0.1.6 note.
-- Without the tag its plaintext content is malformed raw data, which blocks serve and
-- encryption migrate, and encryption prune --corrupt would delete the note.
--
-- Remaining gap: a v0.1.6 caller-chosen edit code that itself starts with "v1$" or "s1$".
UPDATE notes SET legacy_slug = id
WHERE legacy_slug IS NULL
  AND id ~ '^[0-9a-f]{64}$'
  AND left(edit_code, 3) NOT IN ('v1$', 's1$');

-- +goose Down
-- Clear only the tags this migration added: rows that 20261023000001 tags have an id that is
-- not 64 lowercase hex characters, and encryption migrate clears the tag when it converts a row.
UPDATE notes SET legacy_slug = NULL
WHERE legacy_slug IS NOT NULL
  AND id ~ '^[0-9a-f]{64}$';
