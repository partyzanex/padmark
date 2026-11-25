package server

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// seedOlderDB creates a database with only the first migration applied, so a later full migrate
// has both applied and pending migrations (the only case that takes a backup).
func seedOlderDB(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sqlite.Open(dbPath, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	require.NoError(t, err)

	_, err = provider.UpByOne(t.Context())
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

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

type MigrationBackupSuite struct {
	suite.Suite
}

func TestMigrationBackupSuite(t *testing.T) {
	suite.Run(t, new(MigrationBackupSuite))
}

func (s *MigrationBackupSuite) TestMigrateCommand_BacksUpOnlyWhenPending() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")

	s.Require().NoError(runMigrate(s.T(), dbPath))
	s.Require().Empty(backupsOf(s.T(), dbPath), "fresh install has nothing worth backing up")

	dbPath = filepath.Join(s.T().TempDir(), "older.db")
	seedOlderDB(s.T(), dbPath)

	s.Require().NoError(runMigrate(s.T(), dbPath))
	s.Require().Len(backupsOf(s.T(), dbPath), 1)

	s.Require().NoError(runMigrate(s.T(), dbPath))
	s.Require().Len(backupsOf(s.T(), dbPath), 1)
}

func (s *MigrationBackupSuite) TestMigrateCommand_SkipFlag_NoBackup() {
	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	seedOlderDB(s.T(), dbPath)

	s.Require().NoError(runMigrate(s.T(), dbPath, "--skip-migration-backup"))
	s.Require().Empty(backupsOf(s.T(), dbPath))
}

func (s *MigrationBackupSuite) TestMigrateCommand_SkipEnv_NoBackup() {
	s.T().Setenv(EnvSkipMigrationBackup, "true")

	dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
	seedOlderDB(s.T(), dbPath)

	s.Require().NoError(runMigrate(s.T(), dbPath))
	s.Require().Empty(backupsOf(s.T(), dbPath))
}

func (s *MigrationBackupSuite) TestMigrateCommand_InMemory_NoBackup() {
	dir := s.T().TempDir()
	s.T().Chdir(dir)

	s.Require().NoError(runMigrate(s.T(), "file::memory:?cache=shared"))
	s.Require().Empty(backupsOf(s.T(), filepath.Join(dir, "file::memory:")))
}

func (s *MigrationBackupSuite) TestInitRepos_SQLite_BackupHonoursSkip() {
	for _, skip := range []bool{false, true} {
		dbPath := filepath.Join(s.T().TempDir(), "padmark.db")
		seedOlderDB(s.T(), dbPath)

		db, err := openDB(s.T().Context(), "sqlite", dbPath, slog.New(slog.DiscardHandler))
		s.Require().NoError(err)

		_, err = initRepos(s.T().Context(), "sqlite", db, newLogger("error", "text"),
			migrationOptions{dsn: dbPath, skipBackup: skip})
		s.Require().NoError(err)
		s.Require().NoError(db.Close())

		if skip {
			s.Require().Empty(backupsOf(s.T(), dbPath))
		} else {
			s.Require().Len(backupsOf(s.T(), dbPath), 1)
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
