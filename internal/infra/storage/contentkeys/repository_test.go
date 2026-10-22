package contentkeys_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	repository "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

func TestInitializeRollsBackStateWhenKeyCheckInsertFails(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"))
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
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "cas.db"))
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
