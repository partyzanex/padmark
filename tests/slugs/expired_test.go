//go:build integration

package slugs_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
)

// expireRecord moves the expiry of the record id in table into the past without a read.
func expireRecord(t *testing.T, db bun.IDB, table, id string) {
	t.Helper()

	_, err := db.NewUpdate().Table(table).Set("expires_at = ?", time.Now().Add(-time.Hour)).
		Where("id = ?", id).Exec(t.Context())
	require.NoError(t, err)
}

// testExpiredNoteSlug checks audit finding 5: an expired note or link that no one read after its
// expiry does not block a note, a link or a file with its slug. A holder that is not expired
// still blocks.
func testExpiredNoteSlug(t *testing.T, db *bun.DB, objects *s3store.Store) {
	t.Helper()

	env := newSlugEnv(t, db, objects, openGate())

	for _, holder := range []string{"notes", "links"} {
		for _, claimant := range sharedResources() {
			t.Run(holder+"_to_"+claimant, func(t *testing.T) {
				slug := uuid.NewString()
				id := domain.HashSlug(slug)
				_, err := env.create(t.Context(), holder, slug, false)
				require.NoError(t, err)

				_, err = env.create(t.Context(), claimant, slug, false)
				require.ErrorIs(t, err, domain.ErrSlugConflict, "a live holder keeps its slug")

				expireRecord(t, db, holder, id)
				assertReservation(t, db, id, holder)

				_, err = env.create(t.Context(), claimant, slug, false)
				require.NoError(t, err, "an expired holder must not block the slug")
				assertReservation(t, db, id, claimant)
			})
		}
	}

	t.Run("read_after_create", func(t *testing.T) { testExpiredNoteRead(t, env, db) })
	t.Run("burn_timer", func(t *testing.T) { testExpiredBurnTimer(t, env, db) })
}

// testExpiredNoteRead checks the sequence of the audit: the create succeeds before a read of the
// expired note, and the read then returns the new note.
func testExpiredNoteRead(t *testing.T, env *slugEnv, db *bun.DB) {
	t.Helper()

	slug := uuid.NewString()
	_, err := env.create(t.Context(), "notes", slug, false)
	require.NoError(t, err)

	expireRecord(t, db, "notes", domain.HashSlug(slug))

	_, err = env.create(t.Context(), "notes", slug, false)
	require.NoError(t, err, "a create before a read of the expired note")

	note, err := env.notes.Read(t.Context(), slug, openReader())
	require.NoError(t, err, "the read returns the new note")
	require.Nil(t, note.ExpiresAt)
}

// testExpiredBurnTimer checks burn-after-reading notes. An unread note has no expiry and holds
// its slug. After the first read starts the burn timer and the timer ends, the slug is free.
func testExpiredBurnTimer(t *testing.T, env *slugEnv, db *bun.DB) {
	t.Helper()

	slug := uuid.NewString()
	id := domain.HashSlug(slug)
	_, err := env.create(t.Context(), "notes", slug, true)
	require.NoError(t, err)

	_, err = db.NewUpdate().Table("notes").Set("burn_ttl = ?", 60).Where("id = ?", id).Exec(t.Context())
	require.NoError(t, err)

	_, err = env.create(t.Context(), "notes", slug, false)
	require.ErrorIs(t, err, domain.ErrSlugConflict, "an unread burn-after-reading note holds its slug")

	read, err := env.notes.Read(t.Context(), slug, openReader())
	require.NoError(t, err)
	require.NotNil(t, read.ExpiresAt, "the first read starts the burn timer")

	_, err = env.create(t.Context(), "notes", slug, false)
	require.ErrorIs(t, err, domain.ErrSlugConflict, "a running burn timer holds the slug")

	expireRecord(t, db, "notes", id)

	_, err = env.create(t.Context(), "links", slug, false)
	require.NoError(t, err, "an ended burn timer must not block the slug")
	assertReservation(t, db, id, "links")
}

// openReader is the reader of a server without accounts: no privacy check, and a deliberate
// access that may burn a note.
func openReader() domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: true, SkipPrivacy: true}
}
