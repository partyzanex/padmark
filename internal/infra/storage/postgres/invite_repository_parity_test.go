//go:build integration

package postgres

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// The PostgreSQL InviteRepository had no package-level tests; these port
// sqlite/invite_repository_test.go and add the race the Postgres implementation exists for
// (UPDATE ... RETURNING instead of SQLite's single-writer SELECT + UPDATE).

func (s *ParitySuite) TestInvite_StoresHashAndRejectsHashReplay() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	var stored inviteRow

	s.Require().NoError(s.db.NewSelect().Model(&stored).Scan(ctx))

	digest := sha256.Sum256([]byte(plain))
	s.Equal(base64.RawURLEncoding.EncodeToString(digest[:]), stored.TokenHash)
	s.NotEqual(plain, stored.TokenHash)
	s.WithinDuration(time.Now().Add(24*time.Hour), stored.ExpiresAt, 5*time.Second)

	_, err = repo.Consume(ctx, stored.TokenHash, "intruder")
	s.Require().ErrorIs(err, domain.ErrNotFound, "a database digest must not be a usable invite")

	invite, err := repo.Consume(ctx, plain, "member")
	s.Require().NoError(err)
	s.Equal(stored.TokenHash, invite.TokenHash)
	s.Equal(issuer.ID, invite.CreatedBy)
	s.Equal("member", invite.UsedBy)
	s.NotNil(invite.UsedAt)

	_, err = repo.Consume(ctx, plain, "replay")
	s.Require().ErrorIs(err, domain.ErrInviteUsed)
}

func (s *ParitySuite) TestInvite_ExpiredIsRejectedByConsumeAndRedeem() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	_, err = s.db.NewUpdate().Table("invites").Set("expires_at = ?", time.Now().Add(-time.Hour)).
		Where("token_hash = ?", domain.HashInviteToken(plain)).Exec(ctx)
	s.Require().NoError(err)

	_, err = repo.Consume(ctx, plain, "member")
	s.Require().ErrorIs(err, domain.ErrInviteExpired)
	s.Require().ErrorIs(repo.RedeemInvite(ctx, plain, "member", s.parityUser("member")), domain.ErrInviteExpired)
	s.Equal(int64(1), s.count("users"), "an expired invite must not create a user")
}

func (s *ParitySuite) TestInvite_RedeemRollbackAndReplay() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	err = repo.RedeemInvite(ctx, plain, "issuer", s.parityUser("issuer"))
	s.Require().ErrorIs(err, domain.ErrUserExists, "a username conflict must not consume the invite")

	member := s.parityUser("member")
	err = repo.RedeemInvite(ctx, domain.HashInviteToken(plain), member.Username, member)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a database digest must not be a usable invite")

	s.Require().NoError(repo.RedeemInvite(ctx, plain, member.Username, member))

	err = repo.RedeemInvite(ctx, plain, "another", s.parityUser("another"))
	s.Require().ErrorIs(err, domain.ErrInviteUsed)
	s.Equal(int64(2), s.count("users"), "only the issuer and the successful member may exist")
}

func (s *ParitySuite) TestInvite_ConcurrentRedeemIsSingleUse() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	results := race(func(attempt int) error {
		member := s.parityUser(fmt.Sprintf("member-%d", attempt))

		return repo.RedeemInvite(ctx, plain, member.Username, member)
	})

	redeemed := 0

	for _, result := range results {
		if result == nil {
			redeemed++

			continue
		}

		s.Require().ErrorIs(result, domain.ErrInviteUsed, "unexpected failure: %v", result)
	}

	s.Equal(1, redeemed, "an invite must be redeemable exactly once")
	s.Equal(int64(2), s.count("users"))
}

func (s *ParitySuite) TestInvite_ConcurrentConsumeIsSingleUse() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	results := race(func(attempt int) error {
		_, consumeErr := repo.Consume(ctx, plain, fmt.Sprintf("member-%d", attempt))

		return consumeErr
	})

	consumed := 0

	for _, result := range results {
		if result == nil {
			consumed++

			continue
		}

		s.Require().ErrorIs(result, domain.ErrInviteUsed, "unexpected failure: %v", result)
	}

	s.Equal(1, consumed, "an invite must be consumable exactly once")
}

func (s *ParitySuite) TestInvite_IssueSweepsUsedAndExpiredInvites() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	used, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)
	_, err = repo.Consume(ctx, used, "member")
	s.Require().NoError(err)

	expired, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)
	_, err = s.db.NewUpdate().Table("invites").Set("expires_at = ?", time.Now().Add(-time.Minute)).
		Where("token_hash = ?", domain.HashInviteToken(expired)).Exec(ctx)
	s.Require().NoError(err)

	fresh, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	var hashes []string

	s.Require().NoError(s.db.NewSelect().Table("invites").Column("token_hash").Scan(ctx, &hashes))
	s.Equal([]string{domain.HashInviteToken(fresh)}, hashes)
}

func (s *ParitySuite) TestInvite_RevokingIssuerCascadesInvites() {
	ctx := s.T().Context()
	issuer := s.createParityUser("issuer", false)
	repo := NewInviteRepository(s.db, s.discard)

	plain, err := repo.Issue(ctx, issuer.ID)
	s.Require().NoError(err)

	s.Require().NoError(NewUserRepository(s.db).Revoke(ctx, issuer.ID))

	_, err = repo.Consume(ctx, plain, "member")
	s.Require().ErrorIs(err, domain.ErrNotFound, "a revoked issuer's invites must die with them")
}
