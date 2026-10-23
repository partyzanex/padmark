package contentkeys_test

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	repository "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

func TestInitializeRollsBackStateWhenKeyCheckInsertFails(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = sqlite.Migrate(t.Context(), db)
	require.NoError(t, err)

	repo := repository.New(db)
	require.NoError(t, repo.Register(t.Context(), "existing", "original-check"))
	err = repo.Initialize(t.Context(), &keys.Registry{
		InstallationID: "11111111-1111-4111-8111-111111111111", Phase: keys.Pending,
		Checks: map[string]string{"existing": "replacement-check"},
	})
	require.Error(t, err)
	registry, err := repo.Registry(t.Context())
	require.NoError(t, err)
	require.Nil(t, registry, "state and checks must commit atomically")

	var check string
	require.NoError(t, db.NewSelect().Table("content_encryption_keys").Column("verification").
		Where("key_id = ?", "existing").Scan(t.Context(), &check))
	require.Equal(t, "original-check", check)
}

func TestCiphertextCASPreservesConcurrentReplacements(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "cas.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = sqlite.Migrate(t.Context(), db)
	require.NoError(t, err)
	require.NoError(t, sqlite.NewNoteRepository(db).Create(t.Context(), &domain.Note{
		ID: domain.HashSlug("slug"), Content: "old", EditCode: "hash", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	repo := repository.New(db)
	row, err := repo.Get(t.Context(), "note", domain.HashSlug("slug"))
	require.NoError(t, err)
	require.NotNil(t, row)
	changed, err := repo.Replace(t.Context(), *row, "concurrent")
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = repo.Replace(t.Context(), *row, "stale-replacement")
	require.NoError(t, err)
	require.False(t, changed)
	deleted, err := repo.Delete(t.Context(), *row)
	require.NoError(t, err)
	require.False(t, deleted)
	stored, err := repo.Get(t.Context(), "note", row.ID)
	require.NoError(t, err)
	require.Equal(t, "concurrent", stored.Value)
}

// insertV016Note stores a note the way v0.1.6 did (slug as id, plaintext content and edit
// code) and tags it like the legacy_notes schema migration.
func insertV016Note(t *testing.T, db *bun.DB, slug, content, editCode string) {
	t.Helper()

	now := time.Now().UTC()
	_, err := db.NewRaw(`INSERT INTO notes
 (id, title, content, edit_code, views, burn_after_reading, burn_ttl, privacy, created_at, updated_at, legacy_slug)
 VALUES (?, 'title', ?, ?, 0, false, 0, 'public', ?, ?, ?)`,
		slug, content, editCode, now, now, slug).Exec(t.Context())
	require.NoError(t, err)
}

func TestConvertLegacyCASAndTargetConflict(t *testing.T) {
	ctx := t.Context()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqlite.Migrate(ctx, db)
	require.NoError(t, err)

	insertV016Note(t, db, "legacy-slug", "plain $ text", "hunter2")
	insertV016Note(t, db, "taken-slug", "taken plaintext", "Abcdefgh1234")
	require.NoError(t, sqlite.NewNoteRepository(db).Create(ctx, &domain.Note{
		ID: domain.HashSlug("taken-slug"), Content: "current", EditCode: "s1$hash",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	repo := repository.New(db)
	rows, err := repo.Batch(ctx, "note", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, &keys.LegacyNote{Slug: "legacy-slug", EditCode: "hunter2"}, rows[1].Legacy)
	require.Nil(t, rows[0].Legacy, "current notes carry no legacy columns")

	legacy, err := repo.Get(ctx, "note", "legacy-slug")
	require.NoError(t, err)
	require.Equal(t, rows[1], *legacy)

	stale := *legacy
	stale.Value = "edited meanwhile"
	converted, err := repo.ConvertLegacy(ctx, stale, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	require.NoError(t, err)
	require.False(t, converted, "a changed row must not be converted")

	converted, err = repo.ConvertLegacy(ctx, *legacy, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	require.NoError(t, err)
	require.True(t, converted)

	current, err := repo.Get(ctx, "note", domain.HashSlug("legacy-slug"))
	require.NoError(t, err)
	require.Equal(t, keys.Row{Resource: "note", ID: domain.HashSlug("legacy-slug"), Value: "pmc2$p$new"}, *current)

	var editCode string
	require.NoError(t, db.NewSelect().Table("notes").Column("edit_code").Where("id = ?", current.ID).Scan(ctx, &editCode))
	require.Equal(t, "v1$hash", editCode)

	taken, err := repo.Get(ctx, "note", "taken-slug")
	require.NoError(t, err)
	converted, err = repo.ConvertLegacy(ctx, *taken, domain.HashSlug("taken-slug"), "pmc2$p$new", "s1$new")
	require.ErrorIs(t, err, keys.ErrConvertTargetExists)
	require.False(t, converted)
	require.NotContains(t, err.Error(), "taken")

	untouched, err := repo.Get(ctx, "note", "taken-slug")
	require.NoError(t, err)
	require.Equal(t, taken, untouched, "the legacy row stays for manual resolution")

	existing, err := repo.Get(ctx, "note", domain.HashSlug("taken-slug"))
	require.NoError(t, err)
	require.Equal(t, "current", existing.Value, "the existing target is never overwritten")

	_, err = repo.ConvertLegacy(ctx, keys.Row{Resource: "link", ID: "x", Value: "y"}, "z", "v", "")
	require.Error(t, err, "links are never converted")
}
