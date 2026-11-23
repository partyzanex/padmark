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

func (s *UpgradeSuite) TestSQLiteMigrationBackupCollision() {
	t := s.T()

	for _, failVacuum := range []bool{false, true} {
		name := "success"
		if failVacuum {
			name = "vacuum_failure"
		}

		t.Run(name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			path := filepath.Join(t.TempDir(), "old.db")
			original := loadV016SQLite(t, path, log)
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

			existing := reserveBackups(t, path, original)

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

			assertNewBackups(t, files, existing, log)
		})
	}
}

// loadV016SQLite loads the v0.1.6 SQLite dump into the database file path and returns the bytes
// of that file.
func loadV016SQLite(t *testing.T, path string, log *slog.Logger) []byte {
	t.Helper()

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

	return original
}

// reserveBackups writes a copy of original under each backup name in the bounded time window of
// MigrateWithBackup and returns these names. Every second of the window is taken, so the
// collision is deterministic without sleeping until a wall-clock boundary or changing globals.
func reserveBackups(t *testing.T, path string, original []byte) map[string]bool {
	t.Helper()

	existing := make(map[string]bool)

	start := time.Now()
	for second := -1; second <= 6; second++ {
		backup := path + ".bak-" + start.Add(time.Duration(second)*time.Second).Format("20060102-150405")
		require.NoError(t, os.WriteFile(backup, original, 0o600))
		existing[backup] = true
	}

	return existing
}

// assertNewBackups checks each backup in files that is not in existing: mode 0600, and the old
// database with all its notes.
func assertNewBackups(t *testing.T, files []string, existing map[string]bool, log *slog.Logger) {
	t.Helper()

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
}
