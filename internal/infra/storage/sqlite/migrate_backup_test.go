package sqlite

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

func openFileDB(t *testing.T, dsn string) *bun.DB {
	t.Helper()

	db, err := Open(dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.PingContext(t.Context()))

	return db
}

func listBackups(t *testing.T, dbPath string) []string {
	t.Helper()

	matches, err := filepath.Glob(dbPath + ".bak-*")
	require.NoError(t, err)

	return matches
}

// versionRows counts goose_db_version rows, or 0 when the table does not exist yet.
func versionRows(t *testing.T, path string) int {
	t.Helper()

	db := openFileDB(t, path)

	var tables int

	err := db.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'goose_db_version'").Scan(t.Context(), &tables)
	require.NoError(t, err)

	if tables == 0 {
		return 0
	}

	var count int

	require.NoError(t, db.NewRaw("SELECT COUNT(*) FROM goose_db_version").Scan(t.Context(), &count))

	return count
}

func TestMigrateWithBackup_FreshDB_TakesBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")
	db := openFileDB(t, dbPath)

	results, err := MigrateWithBackup(t.Context(), db, dbPath, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotEmpty(t, results)

	backups := listBackups(t, dbPath)
	require.Len(t, backups, 1)

	info, err := os.Stat(backups[0])
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// The backup holds the pre-migration state: strictly fewer versions than the migrated DB.
	require.Less(t, versionRows(t, backups[0]), versionRows(t, dbPath))
}

func TestMigrateWithBackup_SecondRun_NoNewBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")
	db := openFileDB(t, dbPath)
	log := slog.New(slog.DiscardHandler)

	_, err := MigrateWithBackup(t.Context(), db, dbPath, log)
	require.NoError(t, err)

	_, err = MigrateWithBackup(t.Context(), db, dbPath, log)
	require.NoError(t, err)

	require.Len(t, listBackups(t, dbPath), 1)
}

func TestMigrateWithBackup_InMemory_NoBackup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for _, dsn := range []string{":memory:", "file::memory:?cache=shared", "file:mem1?mode=memory&cache=shared"} {
		db := openFileDB(t, dsn)

		results, err := MigrateWithBackup(t.Context(), db, dsn, slog.New(slog.DiscardHandler))
		require.NoError(t, err, dsn)
		require.NotEmpty(t, results, dsn)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestMigrateWithBackup_OlderVersion_TakesBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")
	db := openFileDB(t, dbPath)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	require.NoError(t, err)

	_, err = provider.UpByOne(t.Context())
	require.NoError(t, err)

	_, err = provider.UpByOne(t.Context())
	require.NoError(t, err)

	require.Empty(t, listBackups(t, dbPath))

	before := versionRows(t, dbPath)

	_, err = MigrateWithBackup(t.Context(), db, dbPath, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	backups := listBackups(t, dbPath)
	require.Len(t, backups, 1)
	require.Equal(t, before, versionRows(t, backups[0]))
	require.Less(t, before, versionRows(t, dbPath))
}

func TestMigrateWithBackup_DSNWithQuery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "padmark.db")
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)"
	db := openFileDB(t, dsn)

	_, err := MigrateWithBackup(t.Context(), db, dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Len(t, listBackups(t, dbPath), 1)
}
