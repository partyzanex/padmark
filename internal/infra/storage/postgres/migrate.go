package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"

	pgmigrations "github.com/partyzanex/padmark/migrations/postgres"
)

// Migrate applies all pending goose migrations to the PostgreSQL database.
func Migrate(ctx context.Context, db *bun.DB) ([]*goose.MigrationResult, error) {
	return migrate(ctx, db, nil)
}

// MigrateWithWarning is Migrate that first logs a WARN listing the pending migration versions and
// recommending pg_dump, since PostgreSQL has no automatic backup and some migrations are
// irreversible. Nothing is logged when no migration is pending.
//
// Strategy: fail-hard for the migration itself; a failed pending-status lookup is also returned.
func MigrateWithWarning(ctx context.Context, db *bun.DB, log *slog.Logger) ([]*goose.MigrationResult, error) {
	return migrate(ctx, db, log)
}

func migrate(ctx context.Context, db *bun.DB, log *slog.Logger) ([]*goose.MigrationResult, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, pgmigrations.FS)
	if err != nil {
		return nil, fmt.Errorf("migrations provider: %w", err)
	}

	if log != nil {
		warnErr := warnPending(ctx, provider, log)
		if warnErr != nil {
			return nil, warnErr
		}
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrations up: %w", err)
	}

	return results, nil
}

func warnPending(ctx context.Context, provider *goose.Provider, log *slog.Logger) error {
	statuses, err := provider.Status(ctx)
	if err != nil {
		return fmt.Errorf("migrations status: %w", err)
	}

	var versions []int64

	for _, status := range statuses {
		if status.State == goose.StatePending {
			versions = append(versions, status.Source.Version)
		}
	}

	if len(versions) > 0 {
		log.WarnContext(ctx, "pending database migrations will be applied; some are irreversible, "+
			"take a backup first (e.g. pg_dump)", "pending_versions", versions)
	}

	return nil
}
