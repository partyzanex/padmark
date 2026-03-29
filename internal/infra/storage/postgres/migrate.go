package postgres

import (
	"context"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"

	pgmigrations "github.com/partyzanex/padmark/migrations/postgres"
)

// Migrate applies all pending goose migrations to the PostgreSQL database.
func Migrate(ctx context.Context, db *bun.DB) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, pgmigrations.FS)
	if err != nil {
		return fmt.Errorf("migrations provider: %w", err)
	}

	_, err = provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrations up: %w", err)
	}

	return nil
}
