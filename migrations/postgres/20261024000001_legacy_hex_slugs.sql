-- +goose Up
-- Tag the v0.1.6 notes that 20261023000001_legacy_notes cannot tell apart by id: a custom slug
-- that is itself 64 lowercase hex characters. Such a row looks like a hashed id, but its
-- edit_code is still the plaintext code. Every release after v0.1.6 stores only hashed edit
-- codes, and encryption migrate writes a hash when it converts a row, so an untagged row whose
-- edit_code does not have the full shape of a stored hash can only be a v0.1.6 note. Without
-- the tag its plaintext content is malformed raw data, which blocks serve and encryption
-- migrate.
--
-- The check is the full shape, not a prefix: v0.1.6 accepted any caller-chosen code, including
-- "v1$..." or "s1$...". The pattern equals storedEditCodeHash in internal/infra/crypto/editcode.go:
-- "s1$<salt>$<digest>" or "v1$<memory>$<time>$<threads>$<salt>$<key>", with a 22-character salt
-- and a 43-character digest or key in base64url.
--
-- Remaining gap: a caller-chosen v0.1.6 code with exactly that shape. encryption status then
-- reports the note as corrupt raw data; tag it with `encryption mark-legacy --id <id>`.
UPDATE notes SET legacy_slug = id
WHERE legacy_slug IS NULL
  AND id ~ '^[0-9a-f]{64}$'
  AND edit_code !~ '^(s1|v1\$[0-9]+\$[0-9]+\$[0-9]+)\$[A-Za-z0-9_-]{22}\$[A-Za-z0-9_-]{43}$';

-- +goose Down
-- Clear only the tags this migration added: rows that 20261023000001 tags have an id that is
-- not 64 lowercase hex characters, and encryption migrate clears the tag when it converts a row.
UPDATE notes SET legacy_slug = NULL
WHERE legacy_slug IS NOT NULL
  AND id ~ '^[0-9a-f]{64}$';
