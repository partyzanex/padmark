//go:build integration

package slugs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

func insertResource(ctx context.Context, db bun.IDB, table, id string) error {
	var err error
	if table == "notes" {
		_, err = db.NewRaw(`INSERT INTO notes (id, title, content, created_at, updated_at)
			VALUES (?, 'note', 'test payload', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, id).Exec(ctx)
	} else {
		_, err = db.NewRaw("INSERT INTO links (id, target_url) VALUES (?, 'test ciphertext')", id).Exec(ctx)
	}

	return err
}

func assertReservation(t *testing.T, db bun.IDB, id, resource string) {
	t.Helper()

	var owners []string

	require.NoError(t, db.NewRaw("SELECT resource FROM slug_namespace WHERE id = ?", id).Scan(t.Context(), &owners))

	if resource == "" {
		require.Empty(t, owners)
	} else {
		require.Equal(t, []string{resource}, owners)
	}
}

func testRelease(t *testing.T, db *bun.DB) {
	t.Helper()

	for _, table := range []string{"notes", "links"} {
		for _, operation := range []string{"delete", "burn", "expiry", "sweep"} {
			if operation == "sweep" && table == "notes" {
				continue
			}

			t.Run(table+"_"+operation, func(t *testing.T) {
				id := uuid.NewString()
				require.NoError(t, insertResource(t.Context(), db, table, id))
				assertReservation(t, db, id, table)
				removeResource(t, db, table, id, operation)
				assertReservation(t, db, id, "")

				opposite := "notes"
				if table == "notes" {
					opposite = "links"
				}

				require.NoError(t, insertResource(t.Context(), db, opposite, id))
				assertReservation(t, db, id, opposite)
			})
		}
	}
}

func removeResource(t *testing.T, db *bun.DB, table, id, operation string) {
	t.Helper()

	noteRepo, linkRepo := repositories(db)
	if operation == "delete" {
		_, err := db.NewDelete().Table(table).Where("id = ?", id).Exec(t.Context())
		require.NoError(t, err)

		return
	}

	if operation == "burn" {
		column := "burn_after_click"
		if table == "notes" {
			column = "burn_after_reading"
		}

		_, err := db.NewUpdate().Table(table).Set("? = ?", bun.Ident(column), true).Where("id = ?", id).Exec(t.Context())
		require.NoError(t, err)
	} else {
		_, err := db.NewUpdate().Table(table).Set("expires_at = ?", time.Now().Add(-time.Hour)).
			Where("id = ?", id).Exec(t.Context())
		require.NoError(t, err)
	}

	if operation == "sweep" {
		// LinkRepository.Create runs the production bulk expiry sweep after insertion.
		link, err := linkRepo.GetByID(t.Context(), id)
		require.NoError(t, err)

		link.ID, link.ExpiresAt = uuid.NewString(), nil
		require.NoError(t, linkRepo.Create(t.Context(), link))
	} else if table == "notes" {
		_, err := noteRepo.Consume(t.Context(), id)
		require.NoError(t, err)
	} else {
		_, err := linkRepo.Consume(t.Context(), id)
		require.NoError(t, err)
	}
}

func testRollback(t *testing.T, db *bun.DB) {
	t.Helper()

	id := uuid.NewString()
	transaction, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, insertResource(t.Context(), transaction, "notes", id))
	assertReservation(t, transaction, id, "notes")
	require.NoError(t, transaction.Rollback())
	assertReservation(t, db, id, "")
	require.NoError(t, insertResource(t.Context(), db, "links", id))

	transaction, err = db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = transaction.NewDelete().Table("links").Where("id = ?", id).Exec(t.Context())
	require.NoError(t, err)
	assertReservation(t, transaction, id, "")
	require.NoError(t, transaction.Rollback())
	assertReservation(t, db, id, "links")

	if db.Dialect().Name() == dialect.PG {
		_, err = db.ExecContext(t.Context(), "TRUNCATE notes")
		require.NoError(t, err)
		assertReservation(t, db, id, "links")
		_, err = db.ExecContext(t.Context(), "TRUNCATE links")
		require.NoError(t, err)
		assertReservation(t, db, id, "")
		require.NoError(t, insertResource(t.Context(), db, "notes", id))
	}
}

func testLegacyConversion(t *testing.T, db *bun.DB) {
	t.Helper()

	oldID, newID, occupied := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now()
	_, err := db.NewRaw(`INSERT INTO notes (id, title, created_at, updated_at, legacy_slug)
		VALUES (?, 'legacy', ?, ?, ?)`, oldID, now, now, oldID).Exec(t.Context())
	require.NoError(t, err)
	assertReservation(t, db, oldID, "")
	require.NoError(t, insertResource(t.Context(), db, "links", oldID))
	require.NoError(t, insertResource(t.Context(), db, "links", occupied))

	_, err = db.NewUpdate().Table("notes").Set("id = ?, legacy_slug = NULL", occupied).Where("id = ?", oldID).
		Exec(t.Context())
	require.Error(t, err, "conversion must not collide with a link")
	assertReservation(t, db, occupied, "links")
	assertReservation(t, db, oldID, "links")

	_, err = db.NewUpdate().Table("notes").Set("id = ?, legacy_slug = NULL", newID).Where("id = ?", oldID).
		Exec(t.Context())
	require.NoError(t, err)
	assertReservation(t, db, newID, "notes")
	assertReservation(t, db, oldID, "links")

	_, err = db.NewUpdate().Table("notes").Set("legacy_slug = id").Where("id = ?", newID).Exec(t.Context())
	require.NoError(t, err)
	assertReservation(t, db, newID, "")
	_, err = db.NewUpdate().Table("notes").Set("legacy_slug = NULL").Where("id = ?", newID).Exec(t.Context())
	require.NoError(t, err)
	assertReservation(t, db, newID, "notes")
}

func testConflictPolicies(t *testing.T, db *bun.DB) {
	t.Helper()

	if db.Dialect().Name() != dialect.SQLite {
		return
	}

	for _, policy := range []string{"IGNORE", "REPLACE"} {
		t.Run(policy, func(t *testing.T) {
			id := uuid.NewString()
			require.NoError(t, insertResource(t.Context(), db, "notes", id))
			_, err := db.NewRaw("INSERT OR "+policy+" INTO links (id, target_url) VALUES (?, 'other')", id).
				Exec(t.Context())
			require.Error(t, err)
			assertReservation(t, db, id, "notes")
			exists, err := db.NewSelect().Table("links").Where("id = ?", id).Exists(t.Context())
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}

func testIDChange(t *testing.T, db *bun.DB) {
	t.Helper()

	for _, table := range []string{"notes", "links"} {
		t.Run(table, func(t *testing.T) {
			oldID, newID, occupied := uuid.NewString(), uuid.NewString(), uuid.NewString()

			opposite := "notes"
			if table == "notes" {
				opposite = "links"
			}

			require.NoError(t, insertResource(t.Context(), db, table, oldID))
			require.NoError(t, insertResource(t.Context(), db, opposite, occupied))
			_, err := db.NewUpdate().Table(table).Set("id = ?", occupied).Where("id = ?", oldID).Exec(t.Context())
			require.Error(t, err)
			assertReservation(t, db, oldID, table)
			assertReservation(t, db, occupied, opposite)
			_, err = db.NewUpdate().Table(table).Set("id = ?", newID).Where("id = ?", oldID).Exec(t.Context())
			require.NoError(t, err)
			assertReservation(t, db, oldID, "")
			assertReservation(t, db, newID, table)
			require.NoError(t, insertResource(t.Context(), db, opposite, oldID))
		})
	}
}
