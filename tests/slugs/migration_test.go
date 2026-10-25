//go:build integration

package slugs_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/domain"
	pgmigrations "github.com/partyzanex/padmark/migrations/postgres"
	sqlitemigrations "github.com/partyzanex/padmark/migrations/sqlite"
)

func testBackfill(t *testing.T, db *bun.DB) {
	t.Helper()

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, sqlitemigrations.FS)
	if db.Dialect().Name() == dialect.PG {
		provider, err = goose.NewProvider(goose.DialectPostgres, db.DB, pgmigrations.FS)
	}

	require.NoError(t, err)
	_, err = provider.DownTo(t.Context(), 20261024000001)
	require.NoError(t, err)

	// Pre-existing cross-table duplicates must stop the migration atomically. Neither
	// resource is removed or silently assigned a different URL by a schema migration.
	id := uuid.NewString()
	require.NoError(t, insertResource(t.Context(), db, "notes", id))
	require.NoError(t, insertResource(t.Context(), db, "links", id))

	legacyID := uuid.NewString()
	now := time.Now()
	_, err = db.NewRaw(`INSERT INTO notes (id, title, created_at, updated_at, legacy_slug)
		VALUES (?, 'legacy', ?, ?, ?)`, legacyID, now, now, legacyID).Exec(t.Context())
	require.NoError(t, err)
	require.NoError(t, insertResource(t.Context(), db, "links", legacyID))
	_, err = provider.Up(t.Context())
	require.Error(t, err)
	version, err := provider.GetDBVersion(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 20261024000001, version)

	var count int

	require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM (
		SELECT id FROM notes WHERE id = ? UNION ALL SELECT id FROM links WHERE id = ?
	) AS occupied`, id, id).Scan(t.Context(), &count))
	require.Equal(t, 2, count, "failed migration preserves both conflicting records")
	_, err = db.NewDelete().Table("links").Where("id = ?", id).Exec(t.Context())
	require.NoError(t, err)
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	assertReservation(t, db, id, "notes")
	assertReservation(t, db, legacyID, "links")

	_, linkRepo := repositories(db)
	require.ErrorIs(t, linkRepo.Create(t.Context(), &domain.Link{
		ID: id, TargetURL: "test ciphertext", CreatedAt: time.Now(),
	}), domain.ErrSlugConflict)

	// The new registry is complete, not only the row added for this test.
	require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM (
		SELECT id FROM notes WHERE legacy_slug IS NULL UNION ALL SELECT id FROM links
	) AS resources LEFT JOIN slug_namespace AS ns ON ns.id = resources.id WHERE ns.id IS NULL`).
		Scan(t.Context(), &count))
	require.Zero(t, count)
}
