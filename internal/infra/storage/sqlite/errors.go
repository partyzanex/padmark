package sqlite

import (
	"errors"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// slugNamespaceConflict is the message of RAISE(ABORT, ...) in the slug_namespace triggers.
const slugNamespaceConflict = "slug_namespace_conflict"

// isUniqueViolation reports whether err is a failed UNIQUE or PRIMARY KEY constraint.
// The driver gives the extended result code, so the check does not read the message text.
func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	code := sqliteErr.Code()

	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// isSlugConflict reports whether err means that the slug is already in use.
//
// A slug is the primary key of notes, links, files and slug_namespace. Thus a duplicate slug
// gives SQLITE_CONSTRAINT_PRIMARYKEY. A slug of a different kind of record gives
// SQLITE_CONSTRAINT_TRIGGER from RAISE(ABORT). That code is the same for all triggers, and
// only the message names the trigger, so the message check applies to that code only.
// A failed UNIQUE constraint on a different column, for example files.object_key, is not a
// slug conflict.
func isSlugConflict(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	switch sqliteErr.Code() {
	case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	case sqlite3.SQLITE_CONSTRAINT_TRIGGER:
		return strings.Contains(sqliteErr.Error(), slugNamespaceConflict)
	default:
		return false
	}
}
