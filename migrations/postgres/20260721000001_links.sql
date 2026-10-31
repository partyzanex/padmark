-- +goose Up
-- Short links, mirroring notes' security model: id is HashSlug(slug) (SHA-256), never the
-- plaintext slug itself, and target_url is encrypted at rest with the slug as key material — a
-- DB dump alone cannot recover either the slug or the URL it protects. See domain.Link.
CREATE TABLE IF NOT EXISTS links (
    id               TEXT        NOT NULL PRIMARY KEY,
    target_url       TEXT        NOT NULL,
    privacy          TEXT        NOT NULL DEFAULT 'public',
    edit_code        TEXT        NOT NULL DEFAULT '',
    views            INTEGER     NOT NULL DEFAULT 0,
    burn_after_click BOOLEAN     NOT NULL DEFAULT FALSE,
    expires_at       TIMESTAMPTZ,
    owner_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_links_owner_id ON links(owner_id);
CREATE INDEX IF NOT EXISTS idx_links_expires_at ON links (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_links_expires_at;
DROP INDEX IF EXISTS idx_links_owner_id;
DROP TABLE IF EXISTS links;
