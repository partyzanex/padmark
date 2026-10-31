//go:build integration

// Package upgrade_test replays the documented operator upgrade path on databases written by a
// real padmark v0.1.6 binary (tests/fixtures/v0.1.6): schema migrations, then
// `encryption migrate`, then reads and edits through the current notes.Manager.
package upgrade_test

import (
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

var hashedID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// repositories are the dialect's notes storage and link checker, as wired by serve.
type repositories struct {
	notes notes.Storage
	links notes.LinksChecker
}

// runUpgrade runs on a database loaded from the fixture dump and already brought to the
// current schema by the same Migrate function `padmark-server migrate` uses.
func runUpgrade(t *testing.T, db *bun.DB, repos repositories, fixture []legacyNote) {
	t.Helper()

	assertSchemaKeepsLegacyRows(t, db, fixture)
	resolveUntaggedNotes(t, db, fixture)
	convertContent(t, db, fixture)
	assertStoredRowsConverted(t, db, fixture)

	manager := notes.NewManager(repos.notes, repos.links, lock.NewLocker(), render.NewRenderer(),
		testutil.ContentEncryptor(t, "note"), crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		slog.New(slog.DiscardHandler), true)

	assertNotesReadable(t, manager, fixture)
	assertEditCodes(t, manager, fixture)
	assertLifecycle(t, manager, fixture)
}

// newKeyManager wires contentkeys.Manager like `padmark-server encryption`, with the keyring
// the notes.Manager below reads with.
func newKeyManager(t *testing.T, db *bun.DB) *contentkeys.Manager {
	t.Helper()

	ring := testutil.ContentKeyring()

	noteCipher, err := ring.ForResource("note")
	require.NoError(t, err)

	linkCipher, err := ring.ForResource("link")
	require.NoError(t, err)

	return contentkeys.New(keyrepo.New(db), ring, noteCipher, linkCipher,
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()))
}

// assertSchemaKeepsLegacyRows: schema migrations must neither expire nor rewrite v0.1.6 rows;
// they only tag them for conversion.
func assertSchemaKeepsLegacyRows(t *testing.T, db *bun.DB, fixture []legacyNote) {
	t.Helper()

	for i := range fixture {
		note := &fixture[i]

		t.Run("schema/"+note.title, func(t *testing.T) {
			var expiresAt *time.Time

			err := db.NewSelect().Table("notes").Column("expires_at").Where("id = ?", note.slug).
				Scan(t.Context(), &expiresAt)
			require.NoError(t, err, "the legacy row must survive schema migrations")

			if note.expiresAt == "" {
				assert.Nil(t, expiresAt, "schema migrations must not expire a legacy note")
			} else if assert.NotNil(t, expiresAt) {
				assert.True(t, parseTime(t, note.expiresAt).Equal(*expiresAt), "expires_at changed")
			}

			var legacySlug sql.NullString

			err = db.NewSelect().Table("notes").ColumnExpr("legacy_slug").Where("id = ?", note.slug).
				Scan(t.Context(), &legacySlug)
			require.NoError(t, err)

			if note.untaggedBySchema {
				assert.False(t, legacySlug.Valid, "a code of the exact hash shape cannot be told apart by SQL")

				return
			}

			assert.Equal(t, sql.NullString{String: note.slug, Valid: true}, legacySlug, "the legacy note must be tagged")
		})
	}
}

// resolveUntaggedNotes walks review R1 on the v0.1.6 notes whose custom slug is 64 hex
// characters: the schema migration tags codes that only start like a hash, prune never deletes
// an untagged v0.1.6 note, and mark-legacy is the explicit fix for a code of the exact hash shape.
func resolveUntaggedNotes(t *testing.T, db *bun.DB, fixture []legacyNote) {
	t.Helper()

	ctx := t.Context()
	keyManager := newKeyManager(t, db)
	argon, fast, shaped := &fixture[noteHexArgonPrefix], &fixture[noteHexFastPrefix], &fixture[noteHexHashShaped]

	// Only the exact-shape code escapes the schema migration. A prune run would delete it, so the
	// preview names it; tagged notes are never candidates.
	report, err := keyManager.Status(ctx)
	require.ErrorContains(t, err, "mark-legacy --id")
	assert.Equal(t, len(fixture)-1, report.Plaintext)
	assert.Equal(t, 1, report.CorruptRaw)
	assert.Zero(t, report.UntaggedLegacy)

	preview, err := keyManager.PreviewPrune(ctx)
	require.NoError(t, err)
	assert.Equal(t, []contentkeys.Candidate{{Resource: "note", ID: shaped.slug, Reason: "corrupt_raw"}},
		preview.Candidates)

	// A database migrated by the earlier prefix-only rule left the other two untagged as well:
	// prune then refuses to run and deletes nothing, not even the corrupt-looking note.
	_, err = db.NewUpdate().Table("notes").Set("legacy_slug = NULL").
		Where("id IN (?)", bun.List([]string{argon.slug, fast.slug})).Exec(ctx)
	require.NoError(t, err)

	report, err = keyManager.PruneCorrupt(ctx)
	require.ErrorContains(t, err, "mark-legacy --id")
	assert.Zero(t, report.Pruned)
	assert.Equal(t, 2, report.UntaggedLegacy)

	count, err := db.NewSelect().Table("notes").Count(ctx)
	require.NoError(t, err)
	require.EqualValues(t, len(fixture), count, "prune must not delete any v0.1.6 note")

	preview, err = keyManager.PreviewPrune(ctx)
	require.Error(t, err)
	assert.ElementsMatch(t, []contentkeys.Candidate{
		{Resource: "note", ID: argon.slug, Reason: "untagged_legacy"},
		{Resource: "note", ID: fast.slug, Reason: "untagged_legacy"},
		{Resource: "note", ID: shaped.slug, Reason: "corrupt_raw"},
	}, preview.Candidates)

	for _, note := range []*legacyNote{argon, fast, shaped} {
		_, err = keyManager.MarkLegacy(ctx, note.slug)
		require.NoError(t, err, "mark-legacy %q", note.title)
	}

	report, err = keyManager.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(fixture), report.Plaintext)
}

// convertContent follows the operator order: serve refuses, status counts the plaintext rows,
// a dry run changes nothing, encryption migrate converts, serve starts, a rerun is a no-op.
func convertContent(t *testing.T, db *bun.DB, fixture []legacyNote) {
	t.Helper()

	ctx := t.Context()
	keyManager := newKeyManager(t, db)

	require.ErrorContains(t, keyManager.PrepareServe(ctx), "content migration required")

	report, err := keyManager.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(fixture), report.Plaintext)
	assert.Zero(t, report.Raw)
	assert.Zero(t, report.Failed)
	assert.Zero(t, report.CorruptRaw)
	assert.Equal(t, contentkeys.Pending, report.State)

	dryRun, err := keyManager.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(fixture), dryRun.Plaintext)
	assert.Zero(t, dryRun.Changed)

	var stored string

	custom := &fixture[noteCustomSlug]
	require.NoError(t, db.NewSelect().Table("notes").Column("content").Where("id = ?", custom.slug).
		Scan(ctx, &stored), "a dry run must not convert anything")
	require.Equal(t, custom.content, stored)

	report, err = keyManager.Migrate(ctx)
	require.NoError(t, err)
	assert.Equal(t, contentkeys.Ready, report.State)
	assert.Equal(t, len(fixture), report.Changed)
	assert.Equal(t, len(fixture), report.Native)
	assert.Zero(t, report.Plaintext)
	assert.Zero(t, report.Conflicts)

	require.NoError(t, keyManager.PrepareServe(ctx))

	report, err = keyManager.Migrate(ctx)
	require.NoError(t, err)
	assert.Zero(t, report.Changed, "a second migrate must be a no-op")
	assert.Zero(t, report.Conflicts)
}

type storedNote struct {
	ID         string         `bun:"id"`
	Content    string         `bun:"content"`
	EditCode   string         `bun:"edit_code"`
	LegacySlug sql.NullString `bun:"legacy_slug"`
}

func assertStoredRowsConverted(t *testing.T, db *bun.DB, fixture []legacyNote) {
	t.Helper()

	var rows []storedNote
	require.NoError(t, db.NewSelect().Table("notes").Column("id", "content", "edit_code", "legacy_slug").
		Scan(t.Context(), &rows))
	require.Len(t, rows, len(fixture))

	byID := make(map[string]storedNote, len(rows))
	for _, row := range rows {
		assert.Regexp(t, hashedID, row.ID)
		assert.True(t, strings.HasPrefix(row.Content, "pmc2$p$"), "content must be a native envelope")
		assert.False(t, row.LegacySlug.Valid, "legacy_slug must be cleared")

		byID[row.ID] = row
	}

	for i := range fixture {
		note := &fixture[i]

		row, found := byID[domain.HashSlug(note.slug)]
		if !assert.True(t, found, "note %q must be stored under its hashed slug", note.title) {
			continue
		}

		assert.NotEqual(t, note.editCode, row.EditCode, "edit code of %q must not stay plaintext", note.title)

		assert.True(t, strings.HasPrefix(row.EditCode, "v1$"),
			"legacy edit code of %q must use argon2id regardless of its shape", note.title)
	}
}

// assertNotesReadable compares every surviving note with the fixture without any read side effect.
func assertNotesReadable(t *testing.T, manager *notes.Manager, fixture []legacyNote) {
	t.Helper()

	for i := range fixture {
		if i == noteExpired {
			continue
		}

		want := &fixture[i]

		t.Run("read/"+want.title, func(t *testing.T) {
			got, err := manager.Peek(t.Context(), want.slug)
			require.NoError(t, err)

			assert.Equal(t, want.title, got.Title)
			assert.Equal(t, want.content, got.Content)
			assert.Equal(t, domain.ContentType(want.contentType), *got.ContentType)
			assert.True(t, parseTime(t, want.createdAt).Equal(got.CreatedAt), "created_at")
			assert.Equal(t, want.views, got.Views)
			assert.Equal(t, want.burn, got.BurnAfterReading)
			assert.Equal(t, want.burnTTL, got.BurnTTL)
			assert.Nil(t, got.OwnerID, "legacy notes stay anonymous")

			privacy := domain.PrivacyPublic
			if want.private {
				privacy = domain.PrivacyAuthenticated
			}

			require.NotNil(t, got.Privacy)
			assert.Equal(t, privacy, *got.Privacy)

			if want.expiresAt == "" {
				assert.Nil(t, got.ExpiresAt)
			} else if assert.NotNil(t, got.ExpiresAt) {
				assert.True(t, parseTime(t, want.expiresAt).Equal(*got.ExpiresAt), "expires_at")
			}
		})
	}
}

func assertEditCodes(t *testing.T, manager *notes.Manager, fixture []legacyNote) {
	t.Helper()

	for _, index := range []int{
		noteUnicode, noteCustomSlug, notePrivate, noteHexArgonPrefix, noteHexFastPrefix, noteHexHashShaped,
		noteWeakEditCode,
	} {
		note := &fixture[index]
		update := func(code string) error {
			_, err := manager.Update(t.Context(), note.slug, code, uuid.Nil, &domain.Note{
				Title: note.title + " (edited)", Content: "edited after the upgrade",

				CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
				Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
				EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
				ClearExpiry: false,
			})

			return err
		}

		require.ErrorIs(t, update(note.editCode+"x"), domain.ErrInvalidEditCode, note.title)
		require.ErrorIs(t, update(""), domain.ErrInvalidEditCode, note.title)
		require.NoError(t, update(note.editCode), "the v0.1.6 edit code of %q must still work", note.title)

		edited, err := manager.Peek(t.Context(), note.slug)
		require.NoError(t, err)
		require.Equal(t, "edited after the upgrade", edited.Content)
	}
}

// assertLifecycle checks burn, expiry and view semantics survive the conversion.
func assertLifecycle(t *testing.T, manager *notes.Manager, fixture []legacyNote) {
	t.Helper()

	ctx := t.Context()

	burn := &fixture[noteBurn]
	read, err := manager.Get(ctx, burn.slug)
	require.NoError(t, err)
	require.Equal(t, burn.content, read.Content)
	_, err = manager.Get(ctx, burn.slug)
	require.ErrorIs(t, err, domain.ErrNotFound, "a burn-after-reading note burns on its first read")

	farFuture := &fixture[noteFarFuture]
	read, err = manager.Get(ctx, farFuture.slug)
	require.NoError(t, err)
	require.Equal(t, farFuture.content, read.Content)
	require.NotNil(t, read.ExpiresAt)
	require.True(t, parseTime(t, farFuture.expiresAt).Equal(*read.ExpiresAt))

	expired := &fixture[noteExpired]
	_, err = manager.Get(ctx, expired.slug)
	require.True(t, errors.Is(err, domain.ErrExpired) || errors.Is(err, domain.ErrNotFound),
		"an expired v0.1.6 note must not be revived, got %v", err)
	_, err = manager.Peek(ctx, expired.slug)
	require.ErrorIs(t, err, domain.ErrNotFound)

	viewed := &fixture[noteViewed]
	read, err = manager.View(ctx, viewed.slug)
	require.NoError(t, err)
	require.Equal(t, viewed.views+1, read.Views)
}
