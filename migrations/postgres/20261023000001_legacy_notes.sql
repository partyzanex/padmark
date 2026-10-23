-- +goose Up
-- Tag notes written by padmark v0.1.6 and earlier so `padmark-server encryption migrate` can
-- convert them in place. Those rows store the slug itself as id and the content and edit code
-- in plaintext; every later release stores hex(sha256(slug)) as id, so an id that is not 64
-- lowercase hex characters can only be such a legacy row. legacy_slug keeps the original slug
-- until the conversion re-keys the row (id = hash, native envelope, hashed edit code) and
-- clears it; serve refuses to start while any row is still tagged.
--
-- Known gap: a v0.1.6 custom slug that is itself 64 lowercase hex characters is not tagged.
-- Its plaintext content then falls into the raw/corrupt-raw path of encryption status.
ALTER TABLE notes ADD COLUMN legacy_slug TEXT;
UPDATE notes SET legacy_slug = id WHERE id !~ '^[0-9a-f]{64}$';

-- +goose Down
ALTER TABLE notes DROP COLUMN legacy_slug;
