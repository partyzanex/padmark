package sqlite

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun/driver/sqliteshim"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// versionBeforePrivacy and versionPrivacy are the goose versions (migration filename timestamps)
// immediately before, and of, 20260720000001_notes_privacy.sql — used to apply every earlier
// migration, insert legacy rows, then apply exactly the privacy migration and inspect the result.
const (
	versionBeforePrivacy = 20260716000001
	versionPrivacy       = 20260720000001
)

// MigratePrivacyTestSuite exercises the notes_privacy migration's Up (backfill) and Down through
// the real goose provider — unlike the other repository test suites in this package, which create
// the schema straight from the current Go model and so never actually run this migration's SQL.
type MigratePrivacyTestSuite struct {
	suite.Suite

	sqldb *sql.DB
}

func TestMigratePrivacyTestSuite(t *testing.T) {
	suite.Run(t, new(MigratePrivacyTestSuite))
}

func (s *MigratePrivacyTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared")
	s.Require().NoError(err)

	s.sqldb = sqldb
}

func (s *MigratePrivacyTestSuite) TearDownTest() {
	s.Require().NoError(s.sqldb.Close())
}

func (s *MigratePrivacyTestSuite) provider() *goose.Provider {
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.sqldb, sqlitemigrations.FS)
	s.Require().NoError(err)

	return provider
}

// TestUp_BackfillsPrivacyFromLegacyPrivateColumn verifies that existing rows with private=1
// become privacy='authenticated', and private=0 rows become privacy='public' (the column
// default) — the exact translation notes.Manager relied on before the Privacy enum existed.
func (s *MigratePrivacyTestSuite) TestUp_BackfillsPrivacyFromLegacyPrivateColumn() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionBeforePrivacy)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO notes (id, title, created_at, updated_at, private) VALUES (?, ?, ?, ?, ?)`,
		"legacy-private", "t1", "2026-01-01 00:00:00", "2026-01-01 00:00:00", 1)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO notes (id, title, created_at, updated_at, private) VALUES (?, ?, ?, ?, ?)`,
		"legacy-public", "t2", "2026-01-01 00:00:00", "2026-01-01 00:00:00", 0)
	s.Require().NoError(err)

	_, err = provider.UpTo(ctx, versionPrivacy)
	s.Require().NoError(err)

	var privacy string

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT privacy FROM notes WHERE id = ?`, "legacy-private").Scan(&privacy))
	s.Equal("authenticated", privacy, "private=1 must backfill to privacy='authenticated'")

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT privacy FROM notes WHERE id = ?`, "legacy-public").Scan(&privacy))
	s.Equal("public", privacy, "private=0 must backfill to the column default, privacy='public'")
}

// TestDown_DropsPrivacyColumnOnly verifies the down migration removes privacy without touching
// the deprecated private column, and that data inserted before Up is otherwise undisturbed.
func (s *MigratePrivacyTestSuite) TestDown_DropsPrivacyColumnOnly() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionPrivacy)
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforePrivacy)
	s.Require().NoError(err)

	var privacy string

	err = s.sqldb.QueryRowContext(ctx, `SELECT privacy FROM notes LIMIT 1`).Scan(&privacy)
	s.Require().Error(err, "privacy column must be gone after Down")

	var privateColExists bool

	err = s.sqldb.QueryRowContext(ctx,
		`SELECT 1 FROM pragma_table_info('notes') WHERE name = 'private'`).Scan(&privateColExists)
	s.Require().NoError(err, "the deprecated private column must survive Down untouched")
}
