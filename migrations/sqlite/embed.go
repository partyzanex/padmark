package sqlitemigrations

import "embed"

// FS holds all goose SQL migration files for SQLite.
//
//go:embed *.sql
var FS embed.FS
