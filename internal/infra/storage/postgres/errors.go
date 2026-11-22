package postgres

import (
	"errors"

	"github.com/uptrace/bun/driver/pgdriver"
)

// uniqueViolation is the PostgreSQL SQLSTATE code for a failed unique constraint.
const uniqueViolation = "23505"

// isUniqueViolation reports whether err is a failed unique constraint (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr pgdriver.Error

	return errors.As(err, &pgErr) && pgErr.Field('C') == uniqueViolation
}

// isSlugConflict reports whether err means that the slug is already in use. It reads the
// constraint name from the error, so a failed unique constraint on a different column, for
// example files.object_key, is not a slug conflict.
func isSlugConflict(err error) bool {
	var pgErr pgdriver.Error
	if !errors.As(err, &pgErr) || pgErr.Field('C') != uniqueViolation {
		return false
	}

	// The primary keys below hold a slug. The slug_namespace triggers insert into
	// slug_namespace, so a slug of a different kind of record fails slug_namespace_pkey.
	switch pgErr.Field('n') {
	case "notes_pkey", "links_pkey", "files_pkey", "slug_namespace_pkey":
		return true
	default:
		return false
	}
}
