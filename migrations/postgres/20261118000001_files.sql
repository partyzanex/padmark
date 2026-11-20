-- +goose Up
-- Published files. id is HashSlug(slug), as for notes and links. The content is in object
-- storage under object_key; sealed_meta holds the file name and the content key, encrypted
-- with the installation keyring. A database dump alone does not give the slugs, the names
-- or the content.
CREATE TABLE files (
    id                  TEXT        NOT NULL PRIMARY KEY,
    object_key          TEXT        NOT NULL UNIQUE,
    sealed_meta         TEXT        NOT NULL,
    privacy             TEXT        NOT NULL DEFAULT 'public',
    edit_code           TEXT        NOT NULL DEFAULT '',
    size                BIGINT      NOT NULL DEFAULT 0,
    downloads           INTEGER     NOT NULL DEFAULT 0,
    burn_after_download BOOLEAN     NOT NULL DEFAULT FALSE,
    expires_at          TIMESTAMPTZ,
    owner_id            UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_files_owner_id ON files (owner_id);
-- Each upload removes expired files (files.Manager.sweepExpired), so this index is used.
CREATE INDEX idx_files_expires_at ON files (expires_at) WHERE expires_at IS NOT NULL;

ALTER TABLE slug_namespace DROP CONSTRAINT slug_namespace_resource_check;
ALTER TABLE slug_namespace ADD CONSTRAINT slug_namespace_resource_check
    CHECK (resource IN ('notes', 'links', 'files'));

-- maintain_slug_namespace uses TG_TABLE_NAME, so the same function serves files.
CREATE TRIGGER files_slug_namespace AFTER INSERT OR UPDATE OF id OR DELETE ON files
FOR EACH ROW EXECUTE FUNCTION maintain_slug_namespace();

CREATE TRIGGER files_release_truncated_slugs BEFORE TRUNCATE ON files
FOR EACH STATEMENT EXECUTE FUNCTION release_truncated_slugs();

-- +goose Down
-- The rollback removes the file records. Their objects stay in the bucket and need a manual
-- cleanup.
DELETE FROM slug_namespace WHERE resource = 'files';
DROP TABLE files;
ALTER TABLE slug_namespace DROP CONSTRAINT slug_namespace_resource_check;
ALTER TABLE slug_namespace ADD CONSTRAINT slug_namespace_resource_check
    CHECK (resource IN ('notes', 'links'));
