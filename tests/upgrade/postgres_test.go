//go:build integration

package upgrade_test

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

func (s *UpgradeSuite) TestPostgresFromV016() {
	t := s.T()
	ctx := t.Context()
	fixture := postgresFixture()

	dump, err := os.ReadFile(fixture.path)
	require.NoError(t, err)

	dsn := testenv.Postgres(t).DSN

	// pg_dump empties search_path for its session, so the dump runs on a throwaway pool.
	loader := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	_, err = loader.ExecContext(ctx, string(dump))
	require.NoError(t, err)
	require.NoError(t, loader.Close())

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	notes := append(fixture.notes(), hexSlugNotes()...)
	notes = append(notes, weakEditCodeNote())
	insertV016Notes(t, db, notes[noteCount:])

	_, err = pgrepo.Migrate(ctx, db)
	require.NoError(t, err)

	runUpgrade(t, db, repositories{
		notes: pgrepo.NewNoteRepository(db),
		links: pgrepo.NewLinkRepository(db, slog.New(slog.DiscardHandler)),
	}, notes)
}
