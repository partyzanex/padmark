//go:build integration

package upgrade_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

func TestSQLiteUpgradeFromV016(t *testing.T) {
	ctx := t.Context()
	fixture := sqliteFixture()
	path := filepath.Join(t.TempDir(), "v016.db")

	dump, err := os.ReadFile(fixture.path)
	require.NoError(t, err)

	// The dump switches connection pragmas (foreign_keys=OFF, writable_schema), so it runs on
	// a throwaway handle; the upgrade then opens the file like the server does.
	loader, err := sqliterepo.Open(path)
	require.NoError(t, err)
	_, err = loader.ExecContext(ctx, string(dump))
	require.NoError(t, err)
	require.NoError(t, loader.Close())

	db, err := sqliterepo.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	runUpgrade(t, db, repositories{
		notes: sqliterepo.NewNoteRepository(db),
		links: sqliterepo.NewLinkRepository(db, slog.New(slog.DiscardHandler)),
	}, fixture.notes())
}
