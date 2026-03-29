package pgmigrations

import "embed"

// FS holds all goose SQL migration files for PostgreSQL.
//
//go:embed *.sql
var FS embed.FS
