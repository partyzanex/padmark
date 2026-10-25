package sqlite

import (
	"errors"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isUniqueViolation reports whether err is a SQLite UNIQUE / PRIMARY KEY constraint failure.
// The namespace triggers also explicitly abort on cross-resource collisions so SQLite's
// outer OR IGNORE/REPLACE policy cannot bypass them. Only that named trigger error counts
// as a uniqueness failure; unrelated trigger/constraint errors keep their own meaning.
func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	code := sqliteErr.Code()

	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
		(code == sqlite3.SQLITE_CONSTRAINT_TRIGGER && strings.Contains(sqliteErr.Error(), "slug_namespace_conflict"))
}
