//go:build integration

package storage

import (
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
)

// contractTables are the tables that the contract suites write. TRUNCATE ... CASCADE also
// empties the tables that refer to them, and the TRUNCATE triggers empty slug_namespace.
const contractTables = "users, sessions, api_tokens, invites, reveal_tokens, notes, links, files, slug_namespace"

// TestPostgresContract starts one container for all contract suites. Each test then starts
// from empty tables (see postgresOpener).
func TestPostgresContract(t *testing.T) {
	ctx := t.Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_contract_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = pgrepo.MigrateWithWarning(ctx, db, slog.New(slog.DiscardHandler))
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
			slugs:    pgrepo.NewSlugRepository(db),
			dialect:  dialectPostgres,
		}
	}
}
