//go:build integration

package upgrade_test

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
)

func TestPostgresUpgradeFromV016(t *testing.T) {
	ctx := t.Context()
	fixture := postgresFixture()

	dump, err := os.ReadFile(fixture.path)
	require.NoError(t, err)

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine", tcpostgres.WithDatabase("padmark"),
		tcpostgres.WithUsername("padmark"), tcpostgres.WithPassword("padmark"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// pg_dump empties search_path for its session, so the dump runs on a throwaway pool.
	loader := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	_, err = loader.ExecContext(ctx, string(dump))
	require.NoError(t, err)
	require.NoError(t, loader.Close())

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = pgrepo.Migrate(ctx, db)
	require.NoError(t, err)

	runUpgrade(t, db, repositories{
		notes: pgrepo.NewNoteRepository(db),
		links: pgrepo.NewLinkRepository(db, slog.New(slog.DiscardHandler)),
	}, fixture.notes())
}
