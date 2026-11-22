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

func TestMigrateInviteTokens_InvalidatesOldInvitesAndDowngradeHashes(t *testing.T) {
	db, err := Open("file:"+t.Name()+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)

	const before, version = 20261014000001, 20261014000002

	_, err = provider.UpTo(t.Context(), before)
	require.NoError(t, err)

	issuer := migrationTestUser("issuer")
	require.NoError(t, NewUserRepository(db).Create(t.Context(), issuer))
	_, err = db.NewRaw("INSERT INTO invites (token, created_by, expires_at) VALUES (?, ?, ?)",
		"legacy-bearer", issuer.ID, time.Now().Add(time.Hour)).Exec(t.Context())
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), version)
	require.NoError(t, err)
	count, err := db.NewSelect().Table("invites").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "plaintext-era invites must be invalidated")

	repo := NewInviteRepository(db)
	err = repo.RedeemInvite(t.Context(), domain.HashInviteToken("legacy-bearer"), "member",
		migrationTestUser("member"))
	require.ErrorIs(t, err, domain.ErrNotFound)

	issued := domain.HashInviteToken("new-bearer")
	require.NoError(t, repo.Create(t.Context(), &domain.Invite{
		TokenHash: issued, CreatedBy: issuer.ID, ExpiresAt: time.Now().Add(time.Hour), UsedAt: nil, UsedBy: "",
	}))
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	kept, err := db.NewSelect().Table("invites").Where("token_hash = ? AND used_at IS NULL", issued).Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), kept, "reapplying migrations must preserve newly issued invites")
	_, err = provider.DownTo(t.Context(), before)
	require.NoError(t, err)
	count, err = db.NewSelect().Table("invites").Column("token").Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "stored hashes must not become bearer credentials after downgrade")

	var token string

	err = db.NewSelect().Table("invites").Column("token_hash").Limit(1).Scan(t.Context(), &token)
	require.Error(t, err, "down migration restores the old token column")
}
