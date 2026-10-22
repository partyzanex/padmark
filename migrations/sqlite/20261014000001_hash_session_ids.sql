-- +goose Up
-- Session ids are bearer credentials; a SHA-256 digest cannot be computed in plain SQLite,
-- so plaintext-era rows cannot be upgraded in place — they are invalidated and every existing
-- session is dropped (users re-authenticate). From now on the primary key stores only the
-- base64url-encoded SHA-256 of the raw session id (the api_tokens scheme); the raw value
-- exists solely in the client's session cookie.
ALTER TABLE sessions RENAME COLUMN session_id TO session_hash;
DELETE FROM sessions;

-- +goose Down
-- Raw session ids cannot be recovered from their hashes. Invalidate hashed-era sessions as
-- well, so older binaries cannot accept a stored (possibly leaked) hash as a session cookie
-- after rollback; users re-authenticate.
DELETE FROM sessions;
ALTER TABLE sessions RENAME COLUMN session_hash TO session_id;