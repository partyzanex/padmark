package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

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

	repo := NewRevealRepository(db, slog.Default())
	require.False(t, repo.Consume(t.Context(), "legacy-raw-token", "note-1"))

	issued, err := repo.Issue(t.Context(), "note-1")
	require.NoError(t, err)

	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	require.True(t, repo.Consume(t.Context(), issued, "note-1"), "reapplying migrations keeps new tokens")

	_, err = repo.Issue(t.Context(), "note-1")
	require.NoError(t, err)

	_, err = provider.DownTo(t.Context(), before)
	require.NoError(t, err)

	count, err = db.NewSelect().Table("reveal_tokens").Column("token").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "stored digests must not become bearer tokens after downgrade")
}
