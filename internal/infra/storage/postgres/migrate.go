package postgres

import (
	"context"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"

	pgmigrations "github.com/partyzanex/padmark/migrations/postgres"
)

// Migrate applies all pending goose migrations to the PostgreSQL database.
func Migrate(ctx context.Context, db *bun.DB) ([]*goose.MigrationResult, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, pgmigrations.FS)
	if err != nil {
		return nil, fmt.Errorf("migrations provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrations up: %w", err)
	}

	return results, nil
}
