package server

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func runMigrate(t *testing.T, dsn string, extra ...string) error {
	t.Helper()

	args := append([]string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}, extra...)

	return NewApp().Run(t.Context(), args)
}

func backupsOf(t *testing.T, dbPath string) []string {
	t.Helper()

	matches, err := filepath.Glob(dbPath + ".bak-*")
	require.NoError(t, err)

	return matches
}

func TestMigrateCommand_BacksUpOnlyWhenPending(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")

	require.NoError(t, runMigrate(t, dbPath))
	require.Len(t, backupsOf(t, dbPath), 1)

	require.NoError(t, runMigrate(t, dbPath))
	require.Len(t, backupsOf(t, dbPath), 1)
}

func TestMigrateCommand_SkipFlag_NoBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")

	require.NoError(t, runMigrate(t, dbPath, "--skip-migration-backup"))
	require.Empty(t, backupsOf(t, dbPath))
}

func TestMigrateCommand_SkipEnv_NoBackup(t *testing.T) {
	t.Setenv(EnvSkipMigrationBackup, "true")

	dbPath := filepath.Join(t.TempDir(), "padmark.db")

	require.NoError(t, runMigrate(t, dbPath))
	require.Empty(t, backupsOf(t, dbPath))
}

func TestMigrateCommand_InMemory_NoBackup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	require.NoError(t, runMigrate(t, "file::memory:?cache=shared"))
	require.Empty(t, backupsOf(t, filepath.Join(dir, "file::memory:")))
}

func TestInitRepos_SQLite_BackupHonoursSkip(t *testing.T) {
	for _, skip := range []bool{false, true} {
		dbPath := filepath.Join(t.TempDir(), "padmark.db")

		db, err := openDB(t.Context(), "sqlite", dbPath)
		require.NoError(t, err)

		_, _, err = initRepos(t.Context(), "sqlite", db, newLogger("error", "text"),
			migrationOptions{dsn: dbPath, skipBackup: skip})
		require.NoError(t, err)
		require.NoError(t, db.Close())

		if skip {
			require.Empty(t, backupsOf(t, dbPath))
		} else {
			require.Len(t, backupsOf(t, dbPath), 1)
		}
	}
}

func TestSkipMigrationBackupFlag_OnServeAndMigrate(t *testing.T) {
	for name, flags := range map[string][]cli.Flag{"serve": appFlags(), "migrate": migrateFlags()} {
		found := false

		for _, flag := range flags {
			for _, flagName := range flag.Names() {
				found = found || flagName == FlagSkipMigrationBackup
			}
		}

		require.True(t, found, name)
	}
}
