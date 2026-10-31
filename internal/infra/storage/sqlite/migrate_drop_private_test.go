package sqlite

import (
	"database/sql"
	"log/slog"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// versionBeforeDropPrivate and versionDropPrivate are the goose versions immediately before,
// and of, 20260723000001_drop_private.sql.
const (
	versionBeforeDropPrivate = 20260722000001
	versionDropPrivate       = 20260723000001
)

// MigrateDropPrivateTestSuite exercises the drop_private migration's Up and Down through the
// real goose provider, same rationale as MigratePrivacyTestSuite.
type MigrateDropPrivateTestSuite struct {
	suite.Suite

	sqldb *sql.DB
}

func TestMigrateDropPrivateTestSuite(t *testing.T) {
	suite.Run(t, new(MigrateDropPrivateTestSuite))
}

func (s *MigrateDropPrivateTestSuite) SetupTest() {
	db, err := Open("file::memory:?cache=shared", slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	s.sqldb = db.DB
}

func (s *MigrateDropPrivateTestSuite) TearDownTest() {
	s.Require().NoError(s.sqldb.Close())
}

func (s *MigrateDropPrivateTestSuite) provider() *goose.Provider {
	s.T().Helper()

	provider, err := goose.NewProvider(goose.DialectSQLite3, s.sqldb, sqlitemigrations.FS)
	s.Require().NoError(err)

	return provider
}

// TestUp_DropsPrivateColumn verifies the deprecated private column is gone after Up, while the
// privacy column (and the rest of the row) survives untouched.
func (s *MigrateDropPrivateTestSuite) TestUp_DropsPrivateColumn() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionBeforeDropPrivate)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO notes (id, title, created_at, updated_at, private, privacy) VALUES (?, ?, ?, ?, ?, ?)`,
		"note-1", "t", "2026-01-01 00:00:00", "2026-01-01 00:00:00", 1, "authenticated")
	s.Require().NoError(err)

	_, err = provider.UpTo(ctx, versionDropPrivate)
	s.Require().NoError(err)

	err = s.sqldb.QueryRowContext(ctx, `SELECT private FROM notes LIMIT 1`).Scan(new(sql.NullBool))
	s.Require().Error(err, "private column must be gone after Up")

	var privacy string

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT privacy FROM notes WHERE id = ?`, "note-1").Scan(&privacy),
		"the row inserted before Up must survive untouched")
	s.Equal("authenticated", privacy)
}

// TestDown_RestoresPrivateColumnStructureOnly verifies Down re-adds the private column (for
// goose round-trip consistency) but, being genuinely irreversible, cannot restore the original
// per-row values — every row gets the column default instead.
func (s *MigrateDropPrivateTestSuite) TestDown_RestoresPrivateColumnStructureOnly() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionDropPrivate)
	s.Require().NoError(err)

	_, err = s.sqldb.ExecContext(ctx,
		`INSERT INTO notes (id, title, created_at, updated_at, privacy) VALUES (?, ?, ?, ?, ?)`,
		"note-1", "t", "2026-01-01 00:00:00", "2026-01-01 00:00:00", "owner")
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforeDropPrivate)
	s.Require().NoError(err)

	var private bool

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT private FROM notes WHERE id = ?`, "note-1").Scan(&private),
		"private column must exist again after Down")
	s.False(private, "Down cannot restore data — the column comes back at its default value")
}
