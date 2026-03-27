package migrations

import "embed"

// FS holds all goose SQL migration files.
//
//go:embed *.sql
var FS embed.FS
