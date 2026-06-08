package sqlite

import (
	"errors"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isUniqueViolation reports whether err is a SQLite UNIQUE / PRIMARY KEY constraint failure.
// Uses the typed driver error code rather than message-string matching, matching the check
// in note_repository.go (robust against driver message changes).
func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	code := sqliteErr.Code()

	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
