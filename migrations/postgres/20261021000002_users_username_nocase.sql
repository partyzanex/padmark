-- +goose Up
-- Usernames were unique only byte-for-byte, so "Admin" could be registered next to "admin".
-- Enforce uniqueness on lower(username). Rows keep the spelling they were registered with
-- (it is what the UI shows); lookups compare lower(username) = lower($1) and use this index.
--
-- If two existing users already collide ("admin" and "Admin") the migration aborts with the
-- error below and changes nothing: rename or remove one of the accounts (for example
-- UPDATE users SET username = 'admin2' WHERE id = '<uuid>') and restart. No account is dropped
-- or merged automatically.
--
-- lower() folds according to the database collation, so non-ASCII folding depends on it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM users GROUP BY lower(username) HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'cannot enforce case-insensitive usernames: users whose usernames differ only by case exist (%)',
            (SELECT string_agg(lower(username), ', ') FROM (
                SELECT lower(username) AS username FROM users GROUP BY lower(username) HAVING count(*) > 1
            ) AS dup);
    END IF;
END
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));

-- +goose Down
DROP INDEX IF EXISTS idx_users_username_lower;
