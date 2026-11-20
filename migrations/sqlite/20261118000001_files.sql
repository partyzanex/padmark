-- +goose Up
-- Published files. id is HashSlug(slug), as for notes and links. The content is in object
-- storage under object_key; sealed_meta holds the file name and the content key, encrypted
-- with the installation keyring. A database dump alone does not give the slugs, the names
-- or the content.
CREATE TABLE files (
    id                  TEXT     NOT NULL PRIMARY KEY,
    object_key          TEXT     NOT NULL UNIQUE,
    sealed_meta         TEXT     NOT NULL,
    privacy             TEXT     NOT NULL DEFAULT 'public',
    edit_code           TEXT     NOT NULL DEFAULT '',
    size                INTEGER  NOT NULL DEFAULT 0,
    downloads           INTEGER  NOT NULL DEFAULT 0,
    burn_after_download INTEGER  NOT NULL DEFAULT 0,
    expires_at          DATETIME,
    owner_id            TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_files_owner_id ON files (owner_id);
-- Each upload removes expired files (files.Manager.sweepExpired), so this index is used.
CREATE INDEX idx_files_expires_at ON files (expires_at) WHERE expires_at IS NOT NULL;

-- SQLite cannot change a CHECK constraint, so the table is built again with 'files'.
-- The rename fails while triggers refer to the table, so the triggers are dropped first and
-- then created again without changes.
DROP TRIGGER links_release_slug;
DROP TRIGGER links_move_slug;
DROP TRIGGER links_reserve_slug;
DROP TRIGGER notes_release_slug;
DROP TRIGGER notes_move_slug;
DROP TRIGGER notes_reserve_slug;

CREATE TABLE slug_namespace_new (
    id       TEXT NOT NULL PRIMARY KEY,
    resource TEXT NOT NULL CHECK (resource IN ('notes', 'links', 'files'))
);
INSERT INTO slug_namespace_new (id, resource) SELECT id, resource FROM slug_namespace;
DROP TABLE slug_namespace;
ALTER TABLE slug_namespace_new RENAME TO slug_namespace;

-- +goose StatementBegin
CREATE TRIGGER notes_reserve_slug AFTER INSERT ON notes
WHEN NEW.legacy_slug IS NULL
BEGIN
    -- RAISE keeps the cross-resource check effective even for INSERT OR IGNORE/REPLACE.
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'notes');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'notes');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notes_move_slug AFTER UPDATE OF id, legacy_slug ON notes
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE NEW.legacy_slug IS NULL
      AND EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'notes');

    INSERT INTO slug_namespace (id, resource)
    SELECT NEW.id, 'notes'
    WHERE NEW.legacy_slug IS NULL AND (OLD.legacy_slug IS NOT NULL OR NEW.id <> OLD.id);

    DELETE FROM slug_namespace
    WHERE id = OLD.id AND resource = 'notes' AND OLD.legacy_slug IS NULL
      AND (NEW.legacy_slug IS NOT NULL OR NEW.id <> OLD.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notes_release_slug AFTER DELETE ON notes
WHEN OLD.legacy_slug IS NULL
BEGIN
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'notes';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_reserve_slug AFTER INSERT ON links
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'links');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'links');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_move_slug AFTER UPDATE OF id ON links
WHEN NEW.id <> OLD.id
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'links');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'links');
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'links';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_release_slug AFTER DELETE ON links
BEGIN
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'links';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER files_reserve_slug AFTER INSERT ON files
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'files');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'files');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER files_move_slug AFTER UPDATE OF id ON files
WHEN NEW.id <> OLD.id
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'files');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'files');
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'files';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER files_release_slug AFTER DELETE ON files
BEGIN
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'files';
END;
-- +goose StatementEnd

-- +goose Down
-- The rollback removes the file records. Their objects stay in the bucket and need a manual
-- cleanup.
DROP TRIGGER files_release_slug;
DROP TRIGGER files_move_slug;
DROP TRIGGER files_reserve_slug;
DELETE FROM slug_namespace WHERE resource = 'files';
DROP TABLE files;
DROP TRIGGER links_release_slug;
DROP TRIGGER links_move_slug;
DROP TRIGGER links_reserve_slug;
DROP TRIGGER notes_release_slug;
DROP TRIGGER notes_move_slug;
DROP TRIGGER notes_reserve_slug;

CREATE TABLE slug_namespace_new (
    id       TEXT NOT NULL PRIMARY KEY,
    resource TEXT NOT NULL CHECK (resource IN ('notes', 'links'))
);
INSERT INTO slug_namespace_new (id, resource) SELECT id, resource FROM slug_namespace;
DROP TABLE slug_namespace;
ALTER TABLE slug_namespace_new RENAME TO slug_namespace;

-- +goose StatementBegin
CREATE TRIGGER notes_reserve_slug AFTER INSERT ON notes
WHEN NEW.legacy_slug IS NULL
BEGIN
    -- RAISE keeps the cross-resource check effective even for INSERT OR IGNORE/REPLACE.
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'notes');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'notes');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notes_move_slug AFTER UPDATE OF id, legacy_slug ON notes
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE NEW.legacy_slug IS NULL
      AND EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'notes');

    INSERT INTO slug_namespace (id, resource)
    SELECT NEW.id, 'notes'
    WHERE NEW.legacy_slug IS NULL AND (OLD.legacy_slug IS NOT NULL OR NEW.id <> OLD.id);

    DELETE FROM slug_namespace
    WHERE id = OLD.id AND resource = 'notes' AND OLD.legacy_slug IS NULL
      AND (NEW.legacy_slug IS NOT NULL OR NEW.id <> OLD.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notes_release_slug AFTER DELETE ON notes
WHEN OLD.legacy_slug IS NULL
BEGIN
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'notes';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_reserve_slug AFTER INSERT ON links
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'links');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'links');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_move_slug AFTER UPDATE OF id ON links
WHEN NEW.id <> OLD.id
BEGIN
    SELECT RAISE(ABORT, 'slug_namespace_conflict')
    WHERE EXISTS (SELECT 1 FROM slug_namespace WHERE id = NEW.id AND resource <> 'links');
    INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, 'links');
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'links';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER links_release_slug AFTER DELETE ON links
BEGIN
    DELETE FROM slug_namespace WHERE id = OLD.id AND resource = 'links';
END;
-- +goose StatementEnd
