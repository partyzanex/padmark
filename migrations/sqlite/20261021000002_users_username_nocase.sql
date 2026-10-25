-- +goose Up
-- Usernames were unique only byte-for-byte, so "Admin" could be registered next to "admin".
-- Enforce uniqueness on lower(username). Rows keep the spelling they were registered with
-- (it is what the UI shows); lookups compare lower(username) = lower(?) and use this index.
--
-- If two existing users already collide ("admin" and "Admin"), creating the index fails with a
-- UNIQUE constraint error and the migration aborts without changing anything: rename or remove
-- one of the accounts (for example with sqlite3: UPDATE users SET username = 'admin2' WHERE
-- id = '<uuid>') and restart. No account is dropped or merged automatically.
--
-- SQLite's lower() folds ASCII letters only, so non-ASCII usernames still compare
-- case-sensitively on this backend; PostgreSQL folds according to the database collation.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));

-- +goose Down
DROP INDEX IF EXISTS idx_users_username_lower;
