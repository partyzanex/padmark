//go:build integration

package storage

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

// contractTables are the tables that the contract suites write. TRUNCATE ... CASCADE also
// empties the tables that refer to them, and the TRUNCATE triggers empty slug_namespace.
const contractTables = "users, sessions, api_tokens, invites, reveal_tokens, notes, links, files, slug_namespace"

// TestPostgresContract runs all contract suites on one database of the shared container. Each
// test then starts from empty tables (see postgresOpener).
func TestPostgresContract(t *testing.T) {
	ctx := t.Context()
	db := testenv.Postgres(t).Open(t)

	_, err := pgrepo.MigrateWithWarning(ctx, db, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	runContract(t, postgresOpener(db))
}

// postgresOpener returns an opener that empties the contract tables of db for each test.
func postgresOpener(db *bun.DB) opener {
	return func(t *testing.T) *backend {
		t.Helper()

		_, err := db.ExecContext(t.Context(), "TRUNCATE "+contractTables+" CASCADE")
		require.NoError(t, err)

		return &backend{
			db:       db,
			users:    pgrepo.NewUserRepository(db),
			sessions: pgrepo.NewSessionRepository(db),
			tokens:   pgrepo.NewAPITokenRepository(db),
			invites:  pgrepo.NewInviteRepository(db),
			reveals:  pgrepo.NewRevealRepository(db),
			notes:    pgrepo.NewNoteRepository(db),
			links:    pgrepo.NewLinkRepository(db, slog.New(slog.DiscardHandler)),
			files:    pgrepo.NewFileRepository(db),
			dialect:  dialectPostgres,
		}
	}
}
