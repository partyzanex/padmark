package sqlite

import (
	"context"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// Migrate applies all pending goose migrations to the database.
func Migrate(ctx context.Context, db *bun.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	if err != nil {
		return fmt.Errorf("migrations provider: %w", err)
	}

	_, err = provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrations up: %w", err)
	}

	return nil
}
