package sqlite

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun/driver/sqliteshim"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// versionBeforeLinks and versionLinks are the goose versions (migration filename timestamps)
// immediately before, and of, 20260721000001_links.sql.
const (
	versionBeforeLinks = 20260720000001
	versionLinks       = 20260721000001
)

// MigrateLinksTestSuite exercises the links migration's Up and Down through the real goose
// provider, same rationale as MigratePrivacyTestSuite: the repository test suites in this
// package create their schema straight from the current Go model and never run this SQL.
type MigrateLinksTestSuite struct {
	suite.Suite

	sqldb *sql.DB
}

func TestMigrateLinksTestSuite(t *testing.T) {
	suite.Run(t, new(MigrateLinksTestSuite))
}

func (s *MigrateLinksTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared")
	s.Require().NoError(err)

	s.sqldb = sqldb
}

func (s *MigrateLinksTestSuite) TearDownTest() {
	s.Require().NoError(s.sqldb.Close())
}

func (s *MigrateLinksTestSuite) provider() *goose.Provider {
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.sqldb, sqlitemigrations.FS)
	s.Require().NoError(err)

	return provider
}

// TestUp_CreatesLinksTableWithDefaults verifies the links table exists with the expected columns
// and defaults (privacy='public', views=0, burn_after_click=0) after a bare insert.
func (s *MigrateLinksTestSuite) TestUp_CreatesLinksTableWithDefaults() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionLinks)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO links (id, target_url) VALUES (?, ?)`, "link-1", "ciphertext")
	s.Require().NoError(err)

	var (
		privacy        string
		views          int
		burnAfterClick int
	)

	err = s.sqldb.QueryRowContext(ctx,
		`SELECT privacy, views, burn_after_click FROM links WHERE id = ?`, "link-1").
		Scan(&privacy, &views, &burnAfterClick)
	s.Require().NoError(err)

	s.Equal("public", privacy)
	s.Equal(0, views)
	s.Equal(0, burnAfterClick)
}

// TestDown_DropsLinksTable verifies the down migration removes the links table entirely.
func (s *MigrateLinksTestSuite) TestDown_DropsLinksTable() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionLinks)
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforeLinks)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx, `SELECT 1 FROM links LIMIT 1`)
	s.Require().Error(err, "links table must be gone after Down")
}
