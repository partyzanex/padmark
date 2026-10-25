//go:build integration

package upgrade_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

type failBackupVacuum struct{}

func (failBackupVacuum) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if strings.HasPrefix(event.Query, "VACUUM INTO ") {
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		return canceled
	}

	return ctx
}

func (failBackupVacuum) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestSQLiteMigrationBackupCollision(t *testing.T) {
	for _, failVacuum := range []bool{false, true} {
		name := "success"
		if failVacuum {
			name = "vacuum_failure"
		}

		t.Run(name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			path := filepath.Join(t.TempDir(), "old.db")
			fixture := sqliteFixture()
			dump, err := os.ReadFile(fixture.path)
			require.NoError(t, err)
			loader, err := sqliterepo.Open(path, log)
			require.NoError(t, err)
			_, err = loader.ExecContext(t.Context(), string(dump))
			require.NoError(t, err)
			require.NoError(t, loader.Close())

			original, err := os.ReadFile(path) //nolint:gosec // G304: database in t.TempDir
			require.NoError(t, err)
			db, err := sqliterepo.Open(path, log)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })

			if failVacuum {
				db = db.WithQueryHook(failBackupVacuum{})
			}

			// Reserve every second in the operation's bounded time window: the collision is
			// deterministic without sleeping until a wall-clock boundary or changing globals.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			existing := make(map[string]bool)

			start := time.Now()
			for second := -1; second <= 6; second++ {
				backup := path + ".bak-" + start.Add(time.Duration(second)*time.Second).Format("20060102-150405")
				require.NoError(t, os.WriteFile(backup, original, 0o600)) //nolint:gosec // G703: backup in t.TempDir
				existing[backup] = true
			}

			_, migrateErr := sqliterepo.MigrateWithBackup(ctx, db, path, log)

			for backup := range existing {
				contents, readErr := os.ReadFile(backup) //nolint:gosec // G304: known backup in t.TempDir
				require.NoError(t, readErr, "an existing backup must never be removed")
				require.Equal(t, original, contents, "an existing backup must never be overwritten")
			}

			files, err := filepath.Glob(path + ".bak-*")
			require.NoError(t, err)

			if failVacuum {
				require.ErrorContains(t, migrateErr, "vacuum into")
				require.Len(t, files, len(existing), "remove only this operation's failed backup")

				return
			}

			require.NoError(t, migrateErr)
			require.Len(t, files, len(existing)+1, "a collision must get a unique backup name")

			for _, backup := range files {
				if existing[backup] {
					continue
				}

				info, statErr := os.Stat(backup)
				require.NoError(t, statErr)
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

				saved, openErr := sqliterepo.Open(backup, log)
				require.NoError(t, openErr)
				t.Cleanup(func() { require.NoError(t, saved.Close()) })

				var count int
				require.NoError(t, saved.NewRaw("SELECT COUNT(*) FROM notes").Scan(t.Context(), &count))
				require.Equal(t, noteCount, count, "the new backup must contain the old database")
			}
		})
	}
}
