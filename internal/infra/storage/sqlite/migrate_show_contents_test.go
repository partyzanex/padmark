package sqlite

import (
	"database/sql"
	"log/slog"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"

	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// versionBeforeShowContents and versionShowContents are the goose versions immediately before,
// and of, 20261110000001_notes_show_contents.sql.
const (
	versionBeforeShowContents = 20261025000001
	versionShowContents       = 20261110000001
)

// MigrateShowContentsTestSuite runs the notes_show_contents migration through the real goose
// provider on rows that exist before the column.
type MigrateShowContentsTestSuite struct {
	suite.Suite

	sqldb *sql.DB
}

func TestMigrateShowContentsTestSuite(t *testing.T) {
	suite.Run(t, new(MigrateShowContentsTestSuite))
}

func (s *MigrateShowContentsTestSuite) SetupTest() {
	db, err := Open("file:migrate_show_contents?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	s.sqldb = db.DB
}

func (s *MigrateShowContentsTestSuite) TearDownTest() {
	s.Require().NoError(s.sqldb.Close())
}

func (s *MigrateShowContentsTestSuite) provider() *goose.Provider {
	s.T().Helper()

	provider, err := goose.NewProvider(goose.DialectSQLite3, s.sqldb, sqlitemigrations.FS)
	s.Require().NoError(err)

	return provider
}

func (s *MigrateShowContentsTestSuite) insertOldNote(id string) {
	s.T().Helper()

	_, err := s.sqldb.ExecContext(s.T().Context(),
		`INSERT INTO notes (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, "old", "2026-01-01 00:00:00", "2026-01-01 00:00:00")
	s.Require().NoError(err)
}

// TestUp_ExistingRowsGetFalse verifies that a note stored before the migration reads back with
// the sidebar off, through the repository that the server uses.
func (s *MigrateShowContentsTestSuite) TestUp_ExistingRowsGetFalse() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionBeforeShowContents)
	s.Require().NoError(err)

	s.insertOldNote("old-note")

	_, err = provider.UpTo(ctx, versionShowContents)
	s.Require().NoError(err)

	var stored bool

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT show_contents FROM notes WHERE id = ?`, "old-note").Scan(&stored))
	s.False(stored)

	db, err := Open("file:migrate_show_contents?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	defer func() { s.Require().NoError(db.Close()) }()

	got, err := NewNoteRepository(db).Get(ctx, "old-note")
	s.Require().NoError(err)
	s.False(got.ContentsSidebar())
}

// TestDown_DropsShowContentsColumnOnly verifies that Down removes only the new column.
func (s *MigrateShowContentsTestSuite) TestDown_DropsShowContentsColumnOnly() {
	ctx := s.T().Context()
	provider := s.provider()

	_, err := provider.UpTo(ctx, versionShowContents)
	s.Require().NoError(err)

	s.insertOldNote("kept-note")

	_, err = provider.DownTo(ctx, versionBeforeShowContents)
	s.Require().NoError(err)

	var columns int

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('notes') WHERE name = 'show_contents'`).Scan(&columns))
	s.Zero(columns, "show_contents must be gone after Down")

	var title string

	s.Require().NoError(s.sqldb.QueryRowContext(ctx,
		`SELECT title FROM notes WHERE id = ?`, "kept-note").Scan(&title))
	s.Equal("old", title, "Down must keep the row")
}
