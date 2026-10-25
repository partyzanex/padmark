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
-- "v1$..." or "s1$...". It equals storedEditCodeHash in internal/infra/crypto/editcode.go:
-- "s1$<salt>$<digest>" or "v1$<memory>$<time>$<threads>$<salt>$<key>", with a 22-character salt
-- and a 43-character digest or key in base64url. SQLite has no regular expressions, so the
-- shape is checked by length, the positions of "$" and GLOB character classes:
--   - both formats end with "$" + 22 base64url characters + "$" + 43 base64url characters;
--   - "s1" is the whole prefix before that suffix (69 characters in total);
--   - for "v1", the part between "v1$" and the suffix is three digit groups joined by "$".
--
-- Remaining gap: a caller-chosen v0.1.6 code with exactly that shape. encryption status then
-- reports the note as corrupt raw data; tag it with `encryption mark-legacy --id <id>`.
UPDATE notes SET legacy_slug = id
WHERE legacy_slug IS NULL
  AND length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'
  AND NOT (
    length(edit_code) >= 69
    AND substr(edit_code, length(edit_code) - 66, 1) = '$'
    AND substr(edit_code, length(edit_code) - 65, 22) NOT GLOB '*[^-A-Za-z0-9_]*'
    AND substr(edit_code, length(edit_code) - 43, 1) = '$'
    AND substr(edit_code, length(edit_code) - 42) NOT GLOB '*[^-A-Za-z0-9_]*'
    AND (
      (length(edit_code) = 69 AND substr(edit_code, 1, 2) = 's1')
      OR (
        length(edit_code) >= 75 AND substr(edit_code, 1, 3) = 'v1$'
        AND substr(edit_code, 4, length(edit_code) - 70) GLOB '[0-9]*[0-9]'
        AND substr(edit_code, 4, length(edit_code) - 70) NOT GLOB '*[^0-9$]*'
        AND substr(edit_code, 4, length(edit_code) - 70) NOT GLOB '*$$*'
        AND length(substr(edit_code, 4, length(edit_code) - 70))
          - length(replace(substr(edit_code, 4, length(edit_code) - 70), '$', '')) = 2
      )
    )
  );

-- +goose Down
-- Clear only the tags this migration added: rows that 20261023000001 tags have an id that is
-- not 64 lowercase hex characters, and encryption migrate clears the tag when it converts a row.
UPDATE notes SET legacy_slug = NULL
WHERE legacy_slug IS NOT NULL
  AND length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*';
