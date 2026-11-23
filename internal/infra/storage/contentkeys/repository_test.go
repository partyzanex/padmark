package contentkeys_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	repository "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

type RepositorySuite struct {
	suite.Suite
}

func TestRepositorySuite(t *testing.T) {
	suite.Run(t, new(RepositorySuite))
}

func (s *RepositorySuite) TestInitializeRollsBackStateWhenKeyCheckInsertFails() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "registry.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	_, err = sqlite.Migrate(s.T().Context(), db)
	s.Require().NoError(err)

	repo := repository.New(db)
	s.Require().NoError(repo.Register(s.T().Context(), "existing", "original-check"))
	err = repo.Initialize(s.T().Context(), &keys.Registry{
		InstallationID: "11111111-1111-4111-8111-111111111111", Phase: keys.Pending,
		Checks: map[string]string{"existing": "replacement-check"},
	})
	s.Require().Error(err)
	registry, err := repo.Registry(s.T().Context())
	s.Require().NoError(err)
	s.Require().Nil(registry, "state and checks must commit atomically")

	var check string
	s.Require().NoError(db.NewSelect().Table("content_encryption_keys").Column("verification").
		Where("key_id = ?", "existing").Scan(s.T().Context(), &check))
	s.Require().Equal("original-check", check)
}

func (s *RepositorySuite) TestCiphertextCASPreservesConcurrentReplacements() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "cas.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	_, err = sqlite.Migrate(s.T().Context(), db)
	s.Require().NoError(err)
	s.Require().NoError(sqlite.NewNoteRepository(db).Create(s.T().Context(), &domain.Note{
		ID: domain.HashSlug("slug"), Content: "old", EditCode: "hash", CreatedAt: time.Now(), UpdatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, Title: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: nil,
	}))
	repo := repository.New(db)
	row, err := repo.Get(s.T().Context(), "note", domain.HashSlug("slug"))
	s.Require().NoError(err)
	s.Require().NotNil(row)
	changed, err := repo.Replace(s.T().Context(), *row, "concurrent")
	s.Require().NoError(err)
	s.Require().True(changed)
	changed, err = repo.Replace(s.T().Context(), *row, "stale-replacement")
	s.Require().NoError(err)
	s.Require().False(changed)
	deleted, err := repo.Delete(s.T().Context(), *row)
	s.Require().NoError(err)
	s.Require().False(deleted)
	stored, err := repo.Get(s.T().Context(), "note", row.ID)
	s.Require().NoError(err)
	s.Require().Equal("concurrent", stored.Value)
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

func (s *RepositorySuite) TestConvertLegacyCASAndTargetConflict() {
	ctx := s.T().Context()
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "legacy.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	_, err = sqlite.Migrate(ctx, db)
	s.Require().NoError(err)

	insertV016Note(s.T(), db, "legacy-slug", "plain $ text", "hunter2")
	insertV016Note(s.T(), db, "taken-slug", "taken plaintext", "Abcdefgh1234")
	s.Require().NoError(sqlite.NewNoteRepository(db).Create(ctx, &domain.Note{
		ID: domain.HashSlug("taken-slug"), Content: "current", EditCode: "s1$hash",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, Title: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: nil,
	}))

	repo := repository.New(db)
	rows, err := repo.Batch(ctx, "note", "", 10)
	s.Require().NoError(err)
	s.Require().Len(rows, 3)
	s.Require().Equal(&keys.LegacyNote{Slug: "legacy-slug", EditCode: "hunter2"}, rows[1].Legacy)
	s.Require().Nil(rows[0].Legacy, "current notes carry no legacy columns")

	legacy, err := repo.Get(ctx, "note", "legacy-slug")
	s.Require().NoError(err)
	s.Require().Equal(rows[1], *legacy)

	stale := *legacy
	stale.Value = "edited meanwhile"
	converted, err := repo.ConvertLegacy(ctx, stale, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	s.Require().NoError(err)
	s.Require().False(converted, "a changed row must not be converted")

	converted, err = repo.ConvertLegacy(ctx, *legacy, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	s.Require().NoError(err)
	s.Require().True(converted)

	current, err := repo.Get(ctx, "note", domain.HashSlug("legacy-slug"))
	s.Require().NoError(err)
	s.Require().Equal(keys.Row{
		Resource: "note", ID: domain.HashSlug("legacy-slug"), Value: "pmc2$p$new", StoredEditCode: "v1$hash",

		Legacy: nil,
	}, *current)

	var editCode string
	s.Require().NoError(db.NewSelect().Table("notes").Column("edit_code").Where("id = ?", current.ID).Scan(ctx, &editCode))
	s.Require().Equal("v1$hash", editCode)

	s.checkConvertRefusals(repo)
}

// checkConvertRefusals checks that ConvertLegacy keeps a legacy note whose target ID is taken and
// that it refuses links.
func (s *RepositorySuite) checkConvertRefusals(repo *repository.Repository) {
	ctx := s.T().Context()

	taken, err := repo.Get(ctx, "note", "taken-slug")
	s.Require().NoError(err)
	converted, err := repo.ConvertLegacy(ctx, *taken, domain.HashSlug("taken-slug"), "pmc2$p$new", "s1$new")
	s.Require().ErrorIs(err, keys.ErrConvertTargetExists)
	s.Require().False(converted)
	s.Require().NotContains(err.Error(), "taken")

	untouched, err := repo.Get(ctx, "note", "taken-slug")
	s.Require().NoError(err)
	s.Require().Equal(taken, untouched, "the legacy row stays for manual resolution")

	existing, err := repo.Get(ctx, "note", domain.HashSlug("taken-slug"))
	s.Require().NoError(err)
	s.Require().Equal("current", existing.Value, "the existing target is never overwritten")

	_, err = repo.ConvertLegacy(ctx, keys.Row{
		Resource: "link", ID: "x", Value: "y",
		Legacy: nil, StoredEditCode: "",
	}, "z", "v", "")
	s.Require().Error(err, "links are never converted")
}

// TestMarkLegacyTagsOnlyTheInspectedUntaggedNote: MarkLegacy tags an untagged note only while it
// still holds the inspected content, and refuses links and already tagged notes.
func (s *RepositorySuite) TestMarkLegacyTagsOnlyTheInspectedUntaggedNote() {
	ctx := s.T().Context()
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "mark.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	_, err = sqlite.Migrate(ctx, db)
	s.Require().NoError(err)

	slug := strings.Repeat("a", 64)
	hashShaped := "s1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("B", 43)
	now := time.Now().UTC()
	_, err = db.NewRaw(`INSERT INTO notes
 (id, title, content, edit_code, views, burn_after_reading, burn_ttl, privacy, created_at, updated_at)
 VALUES (?, 'title', 'v0.1.6 plaintext', ?, 0, false, 0, 'public', ?, ?)`, slug, hashShaped, now, now).Exec(ctx)
	s.Require().NoError(err)

	repo := repository.New(db)
	row, err := repo.Get(ctx, "note", slug)
	s.Require().NoError(err)
	s.Require().Nil(row.Legacy)
	s.Require().Equal(hashShaped, row.StoredEditCode, "untagged notes expose the stored edit code")

	stale := *row
	stale.Value = "edited meanwhile"
	marked, err := repo.MarkLegacy(ctx, stale)
	s.Require().NoError(err)
	s.Require().False(marked, "a changed row must not be tagged")

	marked, err = repo.MarkLegacy(ctx, *row)
	s.Require().NoError(err)
	s.Require().True(marked)

	tagged, err := repo.Get(ctx, "note", slug)
	s.Require().NoError(err)
	s.Require().Equal(&keys.LegacyNote{Slug: slug, EditCode: hashShaped}, tagged.Legacy)
	s.Require().Empty(tagged.StoredEditCode)

	_, err = repo.MarkLegacy(ctx, *tagged)
	s.Require().Error(err, "an already tagged note is refused")
	_, err = repo.MarkLegacy(ctx, keys.Row{
		Resource: "link", ID: slug, Value: "x",
		Legacy: nil, StoredEditCode: "",
	})
	s.Require().Error(err, "links never carry v0.1.6 plaintext")
}

// TestFileMetadataIsARotationResource makes sure that key rotation reads and rewrites the
// sealed metadata of files. Without it, a retired key would make the files unreadable. Delete
// never removes a file record (R-27).
func (s *RepositorySuite) TestFileMetadataIsARotationResource() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "files.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	_, err = sqlite.Migrate(s.T().Context(), db)
	s.Require().NoError(err)
	s.Require().NoError(sqlite.NewFileRepository(db).Create(s.T().Context(), &domain.File{
		CreatedAt: time.Now(), ExpiresAt: nil, Privacy: nil, OwnerID: nil, ID: domain.HashSlug("file"),
		ObjectKey: "object", SealedMeta: "old", Name: "", ContentKey: nil, EditCode: "", Size: 0,
		Downloads: 0, BurnAfterDownload: false,
	}))

	repo := repository.New(db)
	rows, err := repo.Batch(s.T().Context(), "file", "", 10)
	s.Require().NoError(err)
	s.Require().Len(rows, 1)
	s.Require().Equal("old", rows[0].Value)

	deleted, err := repo.Delete(s.T().Context(), rows[0])
	s.Require().Error(err, "the object would stay in the bucket without its content key")
	s.Require().False(deleted)

	changed, err := repo.Replace(s.T().Context(), rows[0], "new")
	s.Require().NoError(err)
	s.Require().True(changed)

	row, err := repo.Get(s.T().Context(), "file", domain.HashSlug("file"))
	s.Require().NoError(err)
	s.Require().Equal(keys.Row{
		Resource: "file", ID: domain.HashSlug("file"), Value: "new",
		Legacy: nil, StoredEditCode: "",
	}, *row)
}
