//go:build integration

package storage

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

// openSQLite gives each test a new file database in a temporary directory. A file database,
// not an in-memory one, has the same locking as production, so the race cases are real.
func openSQLite(t *testing.T) *backend {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "contract.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	return &backend{
		db:       db,
		users:    sqliterepo.NewUserRepository(db),
		sessions: sqliterepo.NewSessionRepository(db),
		tokens:   sqliterepo.NewAPITokenRepository(db),
		invites:  sqliterepo.NewInviteRepository(db),
		reveals:  sqliterepo.NewRevealRepository(db),
		notes:    sqliterepo.NewNoteRepository(db),
		links:    sqliterepo.NewLinkRepository(db, log),
		files:    sqliterepo.NewFileRepository(db),
		slugs:    sqliterepo.NewSlugRepository(db),
		dialect:  dialectSQLite,
	}
}

func TestSQLiteContract(t *testing.T) {
	runContract(t, openSQLite)
}
