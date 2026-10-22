package sqlite

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	migrations "github.com/partyzanex/padmark/migrations/sqlite"
)

const (
	usernameNoCaseBefore  = 20261021000001
	usernameNoCaseVersion = 20261021000002
)

func TestMigrateUsernameNoCase_KeepsExistingRowsAndEnforcesUniqueness(t *testing.T) {
	db, err := Open("file:" + t.Name() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	_, err = provider.UpTo(t.Context(), usernameNoCaseBefore)
	require.NoError(t, err)

	repo := NewUserRepository(db)
	require.NoError(t, repo.Create(t.Context(), inviteTestUser("Mixed")))

	_, err = provider.UpTo(t.Context(), usernameNoCaseVersion)
	require.NoError(t, err)

	got, err := repo.GetByUsername(t.Context(), "mixed")
	require.NoError(t, err)
	require.Equal(t, "Mixed", got.Username, "existing rows stay valid and keep their spelling")

	require.ErrorIs(t, repo.Create(t.Context(), inviteTestUser("MIXED")), domain.ErrUserExists)
}

func TestMigrateUsernameNoCase_FailsLoudlyOnExistingCollision(t *testing.T) {
	db, err := Open("file:" + t.Name() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	_, err = provider.UpTo(t.Context(), usernameNoCaseBefore)
	require.NoError(t, err)

	repo := NewUserRepository(db)
	require.NoError(t, repo.Create(t.Context(), inviteTestUser("admin")))
	require.NoError(t, repo.Create(t.Context(), inviteTestUser("Admin")))

	_, err = provider.UpTo(t.Context(), usernameNoCaseVersion)
	require.Error(t, err, "colliding usernames must abort the migration, not be merged or dropped")

	count, err := db.NewSelect().Table("users").Count(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 2, count, "no account is removed")
}
