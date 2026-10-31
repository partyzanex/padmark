package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	migrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// TestMigrateAPITokenIssuer_DownKeepsTokens: SQLite cannot drop a column that takes part in a
// foreign key, so the down migration rebuilds api_tokens. Every token must survive it (the drop
// must not cascade along issued_by), the column must be gone, and up must apply again cleanly.
func TestMigrateAPITokenIssuer_DownKeepsTokens(t *testing.T) {
	db, err := Open("file:"+t.Name()+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	const before, version = 20261019000001, 20261022000001

	_, err = provider.UpTo(t.Context(), version)
	require.NoError(t, err)

	owner := inviteTestUser("owner")
	require.NoError(t, NewUserRepository(db).Create(t.Context(), owner))

	repo := NewAPITokenRepository(db)
	parent := "hash-parent"
	now := time.Now().Truncate(time.Second)

	for _, tok := range []*domain.APIToken{
		{
			UserID: owner.ID, TokenHash: parent, CreatedAt: now,
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		},
		{
			UserID: owner.ID, TokenHash: "hash-child", CreatedAt: now, IssuedBy: &parent,
			ExpiresAt: nil, LastUsedAt: nil,
		},
	} {
		created, createErr := repo.CreateIfUnderLimit(t.Context(), tok, testAPITokenLimit)
		require.NoError(t, createErr)
		require.True(t, created)
	}

	_, err = provider.DownTo(t.Context(), before)
	require.NoError(t, err)

	count, err := db.NewSelect().Table("api_tokens").Count(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 2, count, "the rebuild must not cascade-delete issued tokens")

	// pragma_table_info, not a SELECT: SQLite reads an unknown double-quoted identifier as a
	// string literal, so selecting a dropped column would not fail.
	var columns int

	err = db.NewRaw("SELECT COUNT(*) FROM pragma_table_info('api_tokens') WHERE name = 'issued_by'").
		Scan(t.Context(), &columns)
	require.NoError(t, err)
	require.Zero(t, columns, "down migration removes issued_by")

	_, err = provider.Up(t.Context())
	require.NoError(t, err)

	_, err = repo.GetByHash(t.Context(), "hash-child")
	require.NoError(t, err)
}
