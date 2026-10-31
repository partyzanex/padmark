package sqlite

import (
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

func inviteTestUser(username string) *domain.User {
	return &domain.User{
		ID: uuid.New(), Username: username, CreatedAt: time.Now().UTC(),
		TOTPSecret: "test-secret", PasswordHash: "test-hash", KDFSalt: []byte("test-salt"),

		LastLoginAt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
}

func inviteTestRepository(t *testing.T) (*bun.DB, *InviteRepository, uuid.UUID) {
	t.Helper()

	db, err := Open(filepath.Join(t.TempDir(), "invites.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = Migrate(t.Context(), db)
	require.NoError(t, err)

	issuer := inviteTestUser("issuer")
	require.NoError(t, NewUserRepository(db).Create(t.Context(), issuer))

	return db, NewInviteRepository(db, slog.New(slog.DiscardHandler)), issuer.ID
}

func TestInviteRepository_StoresHashAndRejectsHashReplay(t *testing.T) {
	db, repo, issuer := inviteTestRepository(t)
	plain, err := repo.Issue(t.Context(), issuer)
	require.NoError(t, err)

	var stored inviteRow

	require.NoError(t, db.NewSelect().Model(&stored).Scan(t.Context()))

	digest := sha256.Sum256([]byte(plain))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(digest[:]), stored.TokenHash)
	require.NotEqual(t, plain, stored.TokenHash)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), stored.ExpiresAt, 5*time.Second)
	_, err = repo.Consume(t.Context(), stored.TokenHash, "intruder")
	require.ErrorIs(t, err, domain.ErrNotFound, "a database digest must not be a usable invite")
	invite, err := repo.Consume(t.Context(), plain, "member")
	require.NoError(t, err)
	require.Equal(t, stored.TokenHash, invite.TokenHash)
	require.Equal(t, "member", invite.UsedBy)
	require.NotNil(t, invite.UsedAt)
	_, err = repo.Consume(t.Context(), plain, "replay")
	require.ErrorIs(t, err, domain.ErrInviteUsed)
}

func TestInviteRepository_HashedLookupPreservesExpiry(t *testing.T) {
	db, repo, issuer := inviteTestRepository(t)
	plain, err := repo.Issue(t.Context(), issuer)
	require.NoError(t, err)
	_, err = db.NewUpdate().Table("invites").Set("expires_at = ?", time.Now().Add(-time.Hour)).
		Where("token_hash = ?", domain.HashInviteToken(plain)).Exec(t.Context())
	require.NoError(t, err)
	_, err = repo.Consume(t.Context(), plain, "member")
	require.ErrorIs(t, err, domain.ErrInviteExpired)
	require.ErrorIs(t, repo.RedeemInvite(t.Context(), plain, "member", inviteTestUser("member")), domain.ErrInviteExpired)
}

func TestInviteRepository_RedeemRollbackAndReplay(t *testing.T) {
	db, repo, issuer := inviteTestRepository(t)
	plain, err := repo.Issue(t.Context(), issuer)
	require.NoError(t, err)
	err = repo.RedeemInvite(t.Context(), plain, "issuer", inviteTestUser("issuer"))
	require.ErrorIs(t, err, domain.ErrUserExists, "a username conflict must not consume the invite")

	member := inviteTestUser("member")
	err = repo.RedeemInvite(t.Context(), domain.HashInviteToken(plain), member.Username, member)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, repo.RedeemInvite(t.Context(), plain, member.Username, member))
	err = repo.RedeemInvite(t.Context(), plain, "another", inviteTestUser("another"))
	require.ErrorIs(t, err, domain.ErrInviteUsed)
	count, err := db.NewSelect().Table("users").Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "only the issuer and successful member must exist")
}

func TestInviteRepository_ConcurrentRedeemIsSingleUse(t *testing.T) {
	db, repo, issuer := inviteTestRepository(t)
	plain, err := repo.Issue(t.Context(), issuer)
	require.NoError(t, err)

	results := make([]error, 2)

	var group sync.WaitGroup

	for i, username := range []string{"first", "second"} {
		member := inviteTestUser(username)

		group.Go(func() {
			results[i] = repo.RedeemInvite(t.Context(), plain, member.Username, member)
		})
	}

	group.Wait()

	consumed := 0

	for _, result := range results {
		if result == nil {
			consumed++
		} else {
			require.ErrorIs(t, result, domain.ErrInviteUsed, "unexpected failure: %v", result)
		}
	}

	require.Equal(t, 1, consumed)
	count, err := db.NewSelect().Table("users").Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
}
