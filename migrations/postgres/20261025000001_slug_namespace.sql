-- +goose Up
-- Stop writers until backfill and trigger installation commit together. A shared unique key
-- then arbitrates cross-table races even when writers run in separate server processes.
LOCK TABLE notes, links IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE slug_namespace (
    id       TEXT NOT NULL PRIMARY KEY,
    resource TEXT NOT NULL CHECK (resource IN ('notes', 'links'))
);

-- A tagged v0.1.6 note reserves its hashed ID when encryption migrate converts it, rather
-- than reserving the plaintext ID. Existing native collisions abort without deleting data.
INSERT INTO slug_namespace (id, resource)
SELECT id, 'notes' FROM notes WHERE legacy_slug IS NULL
UNION ALL
SELECT id, 'links' FROM links;

-- +goose StatementBegin
CREATE FUNCTION maintain_slug_namespace() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    reserve_new BOOLEAN := FALSE;
    release_old BOOLEAN := FALSE;
BEGIN
    IF TG_OP <> 'DELETE' THEN
        reserve_new := TG_OP = 'INSERT' OR NEW.id IS DISTINCT FROM OLD.id;
        IF TG_TABLE_NAME = 'notes' THEN
            reserve_new := NEW.legacy_slug IS NULL AND
                (reserve_new OR OLD.legacy_slug IS NOT NULL);
        END IF;
    END IF;

    IF TG_OP <> 'INSERT' THEN
        release_old := TG_OP = 'DELETE' OR NEW.id IS DISTINCT FROM OLD.id;
        IF TG_TABLE_NAME = 'notes' AND TG_OP <> 'DELETE' THEN
            release_old := release_old OR NEW.legacy_slug IS NOT NULL;
        END IF;
    END IF;

    IF reserve_new THEN
        INSERT INTO slug_namespace (id, resource) VALUES (NEW.id, TG_TABLE_NAME);
    END IF;
    IF release_old THEN
        DELETE FROM slug_namespace WHERE id = OLD.id AND resource = TG_TABLE_NAME;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER notes_slug_namespace AFTER INSERT OR UPDATE OF id, legacy_slug OR DELETE ON notes
FOR EACH ROW EXECUTE FUNCTION maintain_slug_namespace();

CREATE TRIGGER links_slug_namespace AFTER INSERT OR UPDATE OF id OR DELETE ON links
FOR EACH ROW EXECUTE FUNCTION maintain_slug_namespace();

-- TRUNCATE does not fire row DELETE triggers. Keep bulk cleanup consistent as well.
-- +goose StatementBegin
CREATE FUNCTION release_truncated_slugs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM slug_namespace WHERE resource = TG_TABLE_NAME;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER notes_release_truncated_slugs BEFORE TRUNCATE ON notes
FOR EACH STATEMENT EXECUTE FUNCTION release_truncated_slugs();

CREATE TRIGGER links_release_truncated_slugs BEFORE TRUNCATE ON links
FOR EACH STATEMENT EXECUTE FUNCTION release_truncated_slugs();

-- +goose Down
DROP TRIGGER links_release_truncated_slugs ON links;
DROP TRIGGER notes_release_truncated_slugs ON notes;
DROP FUNCTION release_truncated_slugs();
DROP TRIGGER links_slug_namespace ON links;
DROP TRIGGER notes_slug_namespace ON notes;
DROP FUNCTION maintain_slug_namespace();
DROP TABLE slug_namespace;
