package sqlite

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// versionBeforeDirectRedirect and versionDirectRedirect are the goose versions immediately
// before, and of, 20260722000001_links_direct_redirect.sql.
const (
	versionBeforeDirectRedirect = 20260721000001
	versionDirectRedirect       = 20260722000001
)

// MigrateLinksDirectRedirectTestSuite exercises the links_direct_redirect migration's Up and
// Down through the real goose provider, same rationale as MigrateLinksTestSuite.
type MigrateLinksDirectRedirectTestSuite struct {
	suite.Suite

	sqldb *sql.DB
}

func TestMigrateLinksDirectRedirectTestSuite(t *testing.T) {
	suite.Run(t, new(MigrateLinksDirectRedirectTestSuite))
}

func (s *MigrateLinksDirectRedirectTestSuite) SetupTest() {
	db, err := Open("file::memory:?cache=shared")
	s.Require().NoError(err)

	s.sqldb = db.DB
}

func (s *MigrateLinksDirectRedirectTestSuite) TearDownTest() {
	s.Require().NoError(s.sqldb.Close())
}

func (s *MigrateLinksDirectRedirectTestSuite) provider() *goose.Provider {
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.sqldb, sqlitemigrations.FS)
	s.Require().NoError(err)

	return provider
}

// TestUp_AddsNullableDirectRedirectColumn verifies the column exists and defaults to NULL
// ("inherit the server default") for a bare insert.
func (s *MigrateLinksDirectRedirectTestSuite) TestUp_AddsNullableDirectRedirectColumn() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionDirectRedirect)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO links (id, target_url) VALUES (?, ?)`, "link-1", "ciphertext")
	s.Require().NoError(err)

	var directRedirect sql.NullBool

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT direct_redirect FROM links WHERE id = ?`, "link-1").Scan(&directRedirect))
	s.False(directRedirect.Valid, "direct_redirect must default to NULL, not a boolean")
}

// TestDown_DropsDirectRedirectColumnOnly verifies the down migration removes only this column,
// leaving the rest of the links table (and its data) untouched.
func (s *MigrateLinksDirectRedirectTestSuite) TestDown_DropsDirectRedirectColumnOnly() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionDirectRedirect)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO links (id, target_url, direct_redirect) VALUES (?, ?, ?)`, "link-1", "ciphertext", 1)
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforeDirectRedirect)
	s.Require().NoError(err)

	err = s.sqldb.QueryRowContext(ctx, `SELECT direct_redirect FROM links LIMIT 1`).Scan(new(sql.NullBool))
	s.Require().Error(err, "direct_redirect column must be gone after Down")

	var targetURL string

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT target_url FROM links WHERE id = ?`, "link-1").Scan(&targetURL),
		"the row inserted before Down must survive untouched")
	s.Equal("ciphertext", targetURL)
}
