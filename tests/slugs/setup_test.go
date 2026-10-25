//go:build integration

package slugs_test

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

func TestSQLiteSlugNamespace(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "namespace.db")
	open := func(t *testing.T) *bun.DB {
		t.Helper()

		db, err := sqliterepo.Open(dsn, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		return db
	}
	db := open(t)
	_, err := sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	runSlugNamespace(t, db, open)
}

func TestPostgresSlugNamespace(t *testing.T) {
	container, err := tcpostgres.Run(t.Context(), "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
	dsn, err := container.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)
	open := func(t *testing.T) *bun.DB {
		t.Helper()

		db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

		t.Cleanup(func() { require.NoError(t, db.Close()) })

		return db
	}
	db := open(t)
	_, err = pgrepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	runSlugNamespace(t, db, open)
}

func runSlugNamespace(t *testing.T, db *bun.DB, open func(*testing.T) *bun.DB) {
	t.Helper()

	for _, winner := range []string{"notes", "links", "simultaneous"} {
		t.Run("concurrent_"+winner, func(t *testing.T) {
			testConcurrentCreate(t, db, open, winner)
		})
	}

	t.Run("release", func(t *testing.T) { testRelease(t, db) })
	t.Run("rollback", func(t *testing.T) { testRollback(t, db) })
	t.Run("id_change", func(t *testing.T) { testIDChange(t, db) })
	t.Run("legacy_conversion", func(t *testing.T) { testLegacyConversion(t, db) })
	t.Run("conflict_policies", func(t *testing.T) { testConflictPolicies(t, db) })
	t.Run("backfill", func(t *testing.T) { testBackfill(t, db) })
}
