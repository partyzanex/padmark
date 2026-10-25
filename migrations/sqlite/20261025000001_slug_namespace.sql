-- +goose Up
-- A single primary key arbitrates note/link slug races across independent servers. Trigger
-- writes belong to the resource's statement/transaction, including DELETE RETURNING burn and
-- expiry sweeps. Existing collisions abort this migration without deleting either resource.
CREATE TABLE slug_namespace (
    id       TEXT NOT NULL PRIMARY KEY,
    resource TEXT NOT NULL CHECK (resource IN ('notes', 'links'))
);

-- Tagged v0.1.6 notes still have plaintext IDs. Reserve their final hashed IDs only when
-- encryption migrate converts them and clears legacy_slug; serve refuses unconverted rows.
INSERT INTO slug_namespace (id, resource)
SELECT id, 'notes' FROM notes WHERE legacy_slug IS NULL
UNION ALL
SELECT id, 'links' FROM links;

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

-- +goose Down
DROP TRIGGER links_release_slug;
DROP TRIGGER links_move_slug;
DROP TRIGGER links_reserve_slug;
DROP TRIGGER notes_release_slug;
DROP TRIGGER notes_move_slug;
DROP TRIGGER notes_reserve_slug;
DROP TABLE slug_namespace;
