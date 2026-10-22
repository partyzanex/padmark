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

func TestMigrateSessionIDs_InvalidatesOldSessionsAndDowngradeHashes(t *testing.T) {
	db, err := Open("file:" + t.Name() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	const before, version = 20261013000001, 20261014000001

	_, err = provider.UpTo(t.Context(), before)
	require.NoError(t, err)

	owner := inviteTestUser("owner")
	require.NoError(t, NewUserRepository(db).Create(t.Context(), owner))
	_, err = db.NewRaw("INSERT INTO sessions (session_id, user_id, expires_at) VALUES (?, ?, ?)",
		"legacy-bearer", owner.ID, time.Now().Add(time.Hour)).Exec(t.Context())
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), version)
	require.NoError(t, err)
	count, err := db.NewSelect().Table("sessions").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "plaintext-era sessions must be invalidated")

	repo := NewSessionRepository(db, slog.New(slog.DiscardHandler))
	_, err = repo.Get(t.Context(), "legacy-bearer")
	require.ErrorIs(t, err, domain.ErrSessionExpired)

	now := time.Now().Truncate(time.Second)
	require.NoError(t, repo.Create(t.Context(), &domain.Session{
		SessionHash: "hashed-era-digest", UserID: owner.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}))
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	_, err = repo.Get(t.Context(), "hashed-era-digest")
	require.NoError(t, err, "reapplying migrations must preserve newly created sessions")
	_, err = provider.DownTo(t.Context(), before)
	require.NoError(t, err)
	count, err = db.NewSelect().Table("sessions").Column("session_id").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "stored hashes must not become session cookies after downgrade")

	var sessionHash string

	err = db.NewSelect().Table("sessions").Column("session_hash").Limit(1).Scan(t.Context(), &sessionHash)
	require.Error(t, err, "down migration restores the old session_id column")
}
