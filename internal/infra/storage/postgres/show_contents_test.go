//go:build integration

package postgres

import (
	"github.com/pressly/goose/v3"

	"github.com/partyzanex/padmark/internal/domain"
	pgmigrations "github.com/partyzanex/padmark/migrations/postgres"
)

// versionBeforeShowContents is the goose version immediately before
// 20261110000001_notes_show_contents.sql.
const versionBeforeShowContents = 20261025000001

func showContentsNote(id string) *domain.Note { return newNote(id, "t", "body") }

// showContentsCases are the create-time values: nil is an old client that does not send the option.
func showContentsCases() []struct {
	value *bool
	id    string
	want  bool
} {
	return []struct {
		value *bool
		id    string
		want  bool
	}{{nil, "sc-nil", false}, {new(false), "sc-false", false}, {new(true), "sc-true", true}}
}

func (s *RepositoryTestSuite) TestShowContents_CreateAndGet() {
	ctx := s.T().Context()

	for _, tcase := range showContentsCases() {
		note := showContentsNote(tcase.id)
		note.ShowContents = tcase.value
		s.Require().NoError(s.repo.Create(ctx, note))

		got, err := s.repo.Get(ctx, tcase.id)
		s.Require().NoError(err)
		s.Require().NotNil(got.ShowContents, tcase.id)
		s.Equal(tcase.want, *got.ShowContents, tcase.id)
	}
}

// TestShowContents_UpdateNilKeepsStoredValue pins the COALESCE contract: an update without the
// option keeps the stored value, and an explicit value replaces it.
func (s *RepositoryTestSuite) TestShowContents_UpdateNilKeepsStoredValue() {
	ctx := s.T().Context()

	note := showContentsNote("sc-update")
	note.ShowContents = new(true)
	s.Require().NoError(s.repo.Create(ctx, note))

	for _, step := range []struct {
		value *bool
		want  bool
	}{{nil, true}, {new(false), false}, {nil, false}, {new(true), true}} {
		update := showContentsNote("sc-update")
		update.ShowContents = step.value
		s.Require().NoError(s.repo.Update(ctx, "sc-update", update))

		got, err := s.repo.Get(ctx, "sc-update")
		s.Require().NoError(err)
		s.Equal(step.want, got.ContentsSidebar())
	}
}

func (s *RepositoryTestSuite) TestShowContents_ConsumeReturnsValue() {
	ctx := s.T().Context()

	note := showContentsNote("sc-burn")
	note.BurnAfterReading = true
	note.ShowContents = new(true)
	s.Require().NoError(s.repo.Create(ctx, note))

	got, err := s.repo.Consume(ctx, "sc-burn", domain.PrivacyPublic)
	s.Require().NoError(err)
	s.True(got.ContentsSidebar())
}

// TestShowContents_ExistingRowDefaultsToFalse covers rows written without the column, for
// example by an older server version: the column default gives false.
func (s *RepositoryTestSuite) TestShowContents_ExistingRowDefaultsToFalse() {
	ctx := s.T().Context()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notes (id, title, content, created_at, updated_at) VALUES (?, ?, ?, now(), now())`,
		"sc-old", "old", "body")
	s.Require().NoError(err)

	got, err := s.repo.Get(ctx, "sc-old")
	s.Require().NoError(err)
	s.False(got.ContentsSidebar())
}

// TestMigrate_ShowContentsDownAndUp runs the migration Down and Up on a row that exists before
// the column: Down removes only the column, and Up gives the row false.
func (s *RepositoryTestSuite) TestMigrate_ShowContentsDownAndUp() {
	ctx := s.T().Context()

	note := showContentsNote("sc-migrate")
	note.ShowContents = new(true)
	s.Require().NoError(s.repo.Create(ctx, note))

	provider, err := goose.NewProvider(goose.DialectPostgres, s.db.DB, pgmigrations.FS)
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforeShowContents)
	s.Require().NoError(err)

	var columns int

	s.Require().NoError(s.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'notes' AND column_name = 'show_contents'`).Scan(&columns))
	s.Zero(columns, "Down must drop show_contents")

	var title string

	s.Require().NoError(s.db.QueryRowContext(ctx, `SELECT title FROM notes WHERE id = ?`, "sc-migrate").Scan(&title))
	s.Equal("t", title, "Down must keep the row")

	_, err = provider.Up(ctx)
	s.Require().NoError(err)

	got, err := s.repo.Get(ctx, "sc-migrate")
	s.Require().NoError(err)
	s.False(got.ContentsSidebar(), "a row from before the migration has false")
}
