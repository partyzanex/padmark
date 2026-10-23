package sqlite

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

const backupTimeLayout = "20060102-150405"

func newProvider(db *bun.DB) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	if err != nil {
		return nil, fmt.Errorf("migrations provider: %w", err)
	}

	return provider, nil
}

// Migrate applies all pending goose migrations to the database without taking a backup.
func Migrate(ctx context.Context, db *bun.DB) ([]*goose.MigrationResult, error) {
	provider, err := newProvider(db)
	if err != nil {
		return nil, err
	}

	return up(ctx, provider)
}

// MigrateWithBackup applies pending migrations like Migrate, but first copies a file-backed
// database to "<path>.bak-<YYYYMMDD-HHMMSS>" (VACUUM INTO, mode 0600) when anything is pending.
// Several migrations are irreversible, so the backup is the only way back.
//
// Strategy: fail-hard. If the backup cannot be written the database is left unmigrated.
// In-memory databases and up-to-date databases are never backed up.
func MigrateWithBackup(
	ctx context.Context, db *bun.DB, dsn string, log *slog.Logger,
) ([]*goose.MigrationResult, error) {
	provider, err := newProvider(db)
	if err != nil {
		return nil, err
	}

	path, _ := dbFilePath(dsn)
	if path != "" {
		pending, pendingErr := countPending(ctx, provider)
		if pendingErr != nil {
			return nil, pendingErr
		}

		if pending > 0 {
			backupPath := path + ".bak-" + time.Now().Format(backupTimeLayout)

			backupErr := backupDB(ctx, db, backupPath)
			if backupErr != nil {
				return nil, fmt.Errorf("backup before migrations: %w", backupErr)
			}

			log.InfoContext(ctx, "database backed up before migrations",
				"backup", backupPath, "pending", pending)
		}
	}

	return up(ctx, provider)
}

func up(ctx context.Context, provider *goose.Provider) ([]*goose.MigrationResult, error) {
	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrations up: %w", err)
	}

	return results, nil
}

func countPending(ctx context.Context, provider *goose.Provider) (int, error) {
	statuses, err := provider.Status(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrations status: %w", err)
	}

	pending := 0

	for _, status := range statuses {
		if status.State == goose.StatePending {
			pending++
		}
	}

	return pending, nil
}

// backupDB writes a consistent copy of the database to backupPath. The target is created owner-only
// up front (VACUUM INTO accepts an existing empty file) so the copy is never world-readable, and
// restricted again afterwards. A partially written copy is removed on failure.
func backupDB(ctx context.Context, db *bun.DB, backupPath string) error {
	err := restrictDBFile(backupPath, true)
	if err != nil {
		return err
	}

	quoted := "'" + strings.ReplaceAll(backupPath, "'", "''") + "'"

	_, err = db.ExecContext(ctx, "VACUUM INTO "+quoted)
	if err != nil {
		removeErr := os.Remove(backupPath) // our own, partially written copy
		if removeErr != nil {
			return fmt.Errorf("vacuum into: %w (remove partial backup: %w)", err, removeErr)
		}

		return fmt.Errorf("vacuum into: %w", err)
	}

	return restrictDBFile(backupPath, false)
}
