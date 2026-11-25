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

func TestMigrateRevealTokens_InvalidatesPlaintextEraTokensAndDowngradeDropsHashes(t *testing.T) {
	db, err := Open("file:"+t.Name()+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	const before, version = 20261019000001, 20261021000001

	_, err = provider.UpTo(t.Context(), before)
	require.NoError(t, err)

	_, err = db.NewRaw("INSERT INTO reveal_tokens (token, note_id, expires_at) VALUES (?, ?, ?)",
		"legacy-raw-token", "note-1", time.Now().Add(time.Minute)).Exec(t.Context())
	require.NoError(t, err)

	_, err = provider.UpTo(t.Context(), version)
	require.NoError(t, err)

	count, err := db.NewSelect().Table("reveal_tokens").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "plaintext-era reveal tokens must be invalidated")

	repo := NewRevealRepository(db)
	now := time.Now()

	consumed, err := repo.Consume(t.Context(), domain.HashRevealToken("legacy-raw-token"), "note-1", now)
	require.NoError(t, err)
	require.False(t, consumed)

	issued := domain.HashRevealToken("new-token")
	require.NoError(t, repo.Create(t.Context(), issued, "note-1", now.Add(time.Minute)))

	_, err = provider.Up(t.Context())
	require.NoError(t, err)

	consumed, err = repo.Consume(t.Context(), issued, "note-1", now)
	require.NoError(t, err)
	require.True(t, consumed, "reapplying migrations keeps new tokens")

	require.NoError(t, repo.Create(t.Context(), domain.HashRevealToken("later-token"), "note-1", now.Add(time.Minute)))

	_, err = provider.DownTo(t.Context(), before)
	require.NoError(t, err)

	count, err = db.NewSelect().Table("reveal_tokens").Column("token").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "stored digests must not become bearer tokens after downgrade")
}
