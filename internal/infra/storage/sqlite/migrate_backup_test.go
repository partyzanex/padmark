package sqlite

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

func openFileDB(t *testing.T, dsn string) *bun.DB {
	t.Helper()

	db, err := Open(dsn, slog.New(slog.DiscardHandler))
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

type MigrateBackupSuite struct {
	suite.Suite
}

func TestMigrateBackupSuite(t *testing.T) {
	suite.Run(t, new(MigrateBackupSuite))
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_FreshDB_NoBackup() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	db := openFileDB(s.T(), dbPath)

	results, err := MigrateWithBackup(s.T().Context(), db, dbPath, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Require().NotEmpty(results)
	s.Require().Empty(listBackups(s.T(), dbPath))
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_BackupIsOwnerOnly() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	db := openFileDB(s.T(), dbPath)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	s.Require().NoError(err)

	_, err = provider.UpByOne(s.T().Context())
	s.Require().NoError(err)

	_, err = MigrateWithBackup(s.T().Context(), db, dbPath, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	backups := listBackups(s.T(), dbPath)
	s.Require().Len(backups, 1)

	info, err := os.Stat(backups[0])
	s.Require().NoError(err)
	s.Require().Equal(os.FileMode(0o600), info.Mode().Perm())
	s.Require().Less(versionRows(s.T(), backups[0]), versionRows(s.T(), dbPath))
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_SecondRun_NoNewBackup() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	db := openFileDB(s.T(), dbPath)
	log := slog.New(slog.DiscardHandler)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	s.Require().NoError(err)

	_, err = provider.UpByOne(s.T().Context())
	s.Require().NoError(err)

	_, err = MigrateWithBackup(s.T().Context(), db, dbPath, log)
	s.Require().NoError(err)

	_, err = MigrateWithBackup(s.T().Context(), db, dbPath, log)
	s.Require().NoError(err)

	s.Require().Len(listBackups(s.T(), dbPath), 1)
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_InMemory_NoBackup() {
	dir := s.T().TempDir()
	s.T().Chdir(dir)

	for _, dsn := range []string{":memory:", "file::memory:?cache=shared", "file:mem1?mode=memory&cache=shared"} {
		db := openFileDB(s.T(), dsn)

		results, err := MigrateWithBackup(s.T().Context(), db, dsn, slog.New(slog.DiscardHandler))
		s.Require().NoError(err, dsn)
		s.Require().NotEmpty(results, dsn)
	}

	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	s.Require().Empty(entries)
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_OlderVersion_TakesBackup() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	db := openFileDB(s.T(), dbPath)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	s.Require().NoError(err)

	_, err = provider.UpByOne(s.T().Context())
	s.Require().NoError(err)

	_, err = provider.UpByOne(s.T().Context())
	s.Require().NoError(err)

	s.Require().Empty(listBackups(s.T(), dbPath))

	before := versionRows(s.T(), dbPath)

	_, err = MigrateWithBackup(s.T().Context(), db, dbPath, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	backups := listBackups(s.T(), dbPath)
	s.Require().Len(backups, 1)
	s.Require().Equal(before, versionRows(s.T(), backups[0]))
	s.Require().Less(before, versionRows(s.T(), dbPath))
}

func (s *MigrateBackupSuite) TestMigrateWithBackup_DSNWithQuery() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)"
	db := openFileDB(s.T(), dsn)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	s.Require().NoError(err)

	_, err = provider.UpByOne(s.T().Context())
	s.Require().NoError(err)

	_, err = MigrateWithBackup(s.T().Context(), db, dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Require().Len(listBackups(s.T(), dbPath), 1)
}
