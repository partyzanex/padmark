//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// InviteSuite is the contract of the invite repository.
type InviteSuite struct {
	repoSuite
}

func (s *InviteSuite) createInvite(hash string, expiresAt time.Time) {
	s.T().Helper()

	issuer := s.createUser("issuer-"+hash, false)
	s.Require().NoError(s.be.invites.Create(s.T().Context(), newInvite(hash, issuer.ID, expiresAt)))
}

func (s *InviteSuite) TestRedeem_CreatesUserAndUsesInvite() {
	ctx := s.T().Context()
	s.createInvite("hash-1", now().Add(time.Hour))

	s.Require().NoError(s.be.invites.RedeemInvite(ctx, "hash-1", "member", newUser("member", false)))

	_, err := s.be.users.GetByUsername(ctx, "member")
	s.Require().NoError(err)

	err = s.be.invites.RedeemInvite(ctx, "hash-1", "replay", newUser("replay", false))
	s.Require().ErrorIs(err, domain.ErrInviteUsed)
}

func (s *InviteSuite) TestRedeem_Unknown_ReturnsErrNotFound() {
	err := s.be.invites.RedeemInvite(s.T().Context(), "unknown", "member", newUser("member", false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *InviteSuite) TestRedeem_Expired_ReturnsErrInviteExpired() {
	ctx := s.T().Context()
	s.createInvite("old", now().Add(-time.Minute))

	err := s.be.invites.RedeemInvite(ctx, "old", "member", newUser("member", false))
	s.Require().ErrorIs(err, domain.ErrInviteExpired)

	_, err = s.be.users.GetByUsername(ctx, "member")
	s.Require().ErrorIs(err, domain.ErrNotFound, "no user without a valid invite")
}

// TestRedeem_UsernameTaken_KeepsInvite pins the rollback: a failed user insert leaves the
// invite unused, so the invited person can try a different name.
func (s *InviteSuite) TestRedeem_UsernameTaken_KeepsInvite() {
	ctx := s.T().Context()
	s.createInvite("hash-1", now().Add(time.Hour))
	s.createUser("taken", false)

	err := s.be.invites.RedeemInvite(ctx, "hash-1", "taken", newUser("taken", false))
	s.Require().ErrorIs(err, domain.ErrUserExists)

	s.Require().NoError(s.be.invites.RedeemInvite(ctx, "hash-1", "free", newUser("free", false)))
}

func (s *InviteSuite) TestRedeem_Concurrent_SingleUse() {
	ctx := s.T().Context()
	s.createInvite("hash-1", now().Add(time.Hour))

	results := race(func(attempt int) error {
		name := fmt.Sprintf("racer%d", attempt)

		return s.be.invites.RedeemInvite(context.WithoutCancel(ctx), "hash-1", name, newUser(name, false))
	})

	won := 0

	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, domain.ErrInviteUsed):
		default:
			s.Failf("unexpected redeem error", "%v", err)
		}
	}

	s.Equal(1, won)
}

func (s *InviteSuite) TestDeleteStale_RemovesUsedAndExpired() {
	ctx := s.T().Context()
	instant := now()
	s.createInvite("live", instant.Add(time.Hour))
	s.createInvite("expired", instant.Add(-time.Minute))
	s.createInvite("used", instant.Add(time.Hour))
	s.Require().NoError(s.be.invites.RedeemInvite(ctx, "used", "member", newUser("member", false)))

	s.Require().NoError(s.be.invites.DeleteStale(ctx, instant))

	var hashes []string

	s.Require().NoError(s.be.db.NewSelect().Table("invites").Column("token_hash").Scan(ctx, &hashes))
	s.Equal([]string{"live"}, hashes)
}

// TestCreate_DoesNotDeleteStale pins C5: the cleanup is an explicit call of the manager.
func (s *InviteSuite) TestCreate_DoesNotDeleteStale() {
	ctx := s.T().Context()
	s.createInvite("expired", now().Add(-time.Minute))
	s.createInvite("live", now().Add(time.Hour))

	count, err := s.be.db.NewSelect().Table("invites").Count(ctx)
	s.Require().NoError(err)
	s.Equal(int64(2), count)
}
