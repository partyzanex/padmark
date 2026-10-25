package sqlite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// database to "<path>.bak-<YYYYMMDD-HHMMSS>" (VACUUM INTO, mode 0600) when it already has applied
// migrations and more are pending. A collision gets a unique suffix; existing backups are preserved.
// Several migrations are irreversible, so the backup is the only way back.
//
// Strategy: fail-hard. If the backup cannot be written the database is left unmigrated.
// In-memory, brand-new (nothing applied yet) and up-to-date databases are never backed up.
func MigrateWithBackup(
	ctx context.Context, db *bun.DB, dsn string, log *slog.Logger,
) ([]*goose.MigrationResult, error) {
	provider, err := newProvider(db)
	if err != nil {
		return nil, err
	}

	path, _ := dbFilePath(dsn)
	if path != "" {
		applied, pending, countErr := countMigrations(ctx, provider)
		if countErr != nil {
			return nil, countErr
		}

		// A brand-new install (nothing applied yet) has no data worth a backup.
		if applied > 0 && pending > 0 {
			backupPath := path + ".bak-" + time.Now().Format(backupTimeLayout)

			backupPath, backupErr := backupDB(ctx, db, backupPath, log)
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

func countMigrations(ctx context.Context, provider *goose.Provider) (applied, pending int, err error) {
	statuses, err := provider.Status(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("migrations status: %w", err)
	}

	for _, status := range statuses {
		if status.State == goose.StatePending {
			pending++
		} else {
			applied++
		}
	}

	return applied, pending, nil
}

// backupDB reserves an owner-only file exclusively before VACUUM INTO. A collision gets a
// unique suffix. Only the file reserved by this call is removed if writing the copy fails.
func backupDB(ctx context.Context, db *bun.DB, preferred string, log *slog.Logger) (string, error) {
	backupPath, err := reserveBackupFile(preferred)
	if err != nil {
		return "", err
	}

	quoted := "'" + strings.ReplaceAll(backupPath, "'", "''") + "'"

	_, err = db.ExecContext(ctx, "VACUUM INTO "+quoted)
	if err != nil {
		removeErr := os.Remove(backupPath) // our own, partially written copy
		if removeErr != nil {
			return "", fmt.Errorf("vacuum into: %w (remove partial backup: %w)", err, removeErr)
		}

		return "", fmt.Errorf("vacuum into: %w", err)
	}

	return backupPath, restrictDBFile(backupPath, false, log)
}

func reserveBackupFile(preferred string) (string, error) {
	file, err := os.OpenFile(preferred, os.O_RDWR|os.O_CREATE|os.O_EXCL, dbFilePerm) //nolint:gosec // operator DB path
	if errors.Is(err, os.ErrExist) {
		file, err = os.CreateTemp(filepath.Dir(preferred), filepath.Base(preferred)+"-")
	}

	if err != nil {
		return "", fmt.Errorf("reserve sqlite backup: %w", err)
	}

	path := file.Name()

	closeErr := file.Close()
	if closeErr != nil {
		return "", fmt.Errorf("close reserved sqlite backup: %w", errors.Join(closeErr, os.Remove(path)))
	}

	return path, nil
}
