//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// The tests in this file pin the one expiry rule of domain.Note.Expired, domain.Link.Expired,
// domain.File.Expired and the account records on both dialects: a record is expired from its
// expires_at moment on. One second before that moment it is live.

func (s *NoteSuite) TestExpiryBoundary_ExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	before := expires.Add(-time.Second)
	s.Require().NoError(s.be.notes.Create(ctx, newNote("edge", "edit-hash", &expires)))

	s.Require().ErrorIs(s.be.notes.DeleteExpired(ctx, "edge", before), domain.ErrNotFound, "live before expires_at")
	_, err := s.be.notes.Consume(ctx, "edge", domain.PrivacyPublic, "edit-hash", before)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a live note without burn is not consumed")

	update := newNote("edge", "edit-hash", &expires)
	update.Title = "changed"
	s.Require().NoError(s.be.notes.Update(ctx, "edge", update, before), "a live note takes the update")
	s.Require().ErrorIs(s.be.notes.Update(ctx, "edge", update, expires), domain.ErrNotFound,
		"an update at expires_at finds an expired note")

	consumed, err := s.be.notes.Consume(ctx, "edge", domain.PrivacyPublic, "edit-hash", expires)
	s.Require().NoError(err, "the note is expired at expires_at")
	s.Equal("changed", consumed.Title)
}

func (s *NoteSuite) TestExpiryBoundary_DeleteExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("edge", "edit-hash", &expires)))

	s.Require().NoError(s.be.notes.DeleteExpired(ctx, "edge", expires))
}

func (s *LinkSuite) TestExpiryBoundary_ExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	before := expires.Add(-time.Second)
	s.Require().NoError(s.be.links.Create(ctx, newLink("edge", &expires)))

	s.Require().ErrorIs(s.be.links.DeleteExpired(ctx, "edge", before), domain.ErrNotFound, "live before expires_at")
	_, err := s.be.links.Consume(ctx, "edge", before)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a live link without burn is not consumed")

	patch := &domain.LinkPatch{
		TargetURL: "new-ciphertext", ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ClearExpiry: false, ResetDirectRedirect: false,
	}
	_, err = s.be.links.Update(ctx, "edge", "edit-hash", patch, before)
	s.Require().NoError(err, "a live link takes the update")
	_, err = s.be.links.Update(ctx, "edge", "edit-hash", patch, expires)
	s.Require().ErrorIs(err, domain.ErrNotFound, "an update at expires_at finds an expired link")

	consumed, err := s.be.links.Consume(ctx, "edge", expires)
	s.Require().NoError(err, "the link is expired at expires_at")
	s.Equal("new-ciphertext", consumed.TargetURL)
}

func (s *LinkSuite) TestExpiryBoundary_DeleteExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	s.Require().NoError(s.be.links.Create(ctx, newLink("edge", &expires)))

	s.Require().NoError(s.be.links.DeleteExpired(ctx, "edge", expires))
}

// TestCreate_SweepUsesCreatedAt pins that the sweep in Create uses the creation time of the new
// link as its clock, with the same expiry rule.
func (s *LinkSuite) TestCreate_SweepUsesCreatedAt() {
	ctx := s.T().Context()
	expires := now()
	s.Require().NoError(s.be.links.Create(ctx, newLink("at-edge", &expires)))

	later := expires.Add(time.Second)
	s.Require().NoError(s.be.links.Create(ctx, newLink("live", &later)))

	newer := newLink("new", nil)
	newer.CreatedAt = expires
	s.Require().NoError(s.be.links.Create(ctx, newer))

	_, err := s.be.links.GetByID(ctx, "at-edge")
	s.Require().ErrorIs(err, domain.ErrNotFound, "the link that is expired at CreatedAt is swept")

	_, err = s.be.links.GetByID(ctx, "live")
	s.Require().NoError(err, "a link that expires after CreatedAt stays")
}

func (s *FileSuite) TestExpiryBoundary_ExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	before := expires.Add(-time.Second)
	s.Require().NoError(s.be.files.Create(ctx, newFile("edge", "object-edge", &expires)))

	listed, err := s.be.files.ListExpired(ctx, before, files.ExpiredCursor{ExpiresAt: time.Time{}, ID: ""}, 10)
	s.Require().NoError(err)
	s.Empty(listed, "live before expires_at")

	listed, err = s.be.files.ListExpired(ctx, expires, files.ExpiredCursor{ExpiresAt: time.Time{}, ID: ""}, 10)
	s.Require().NoError(err)
	s.Equal([]string{"edge"}, fileIDs(listed), "expired at expires_at")

	privacy := domain.PrivacyPublic
	opts := files.UpdateOptions{ExpiresAt: nil, Privacy: &privacy, BurnAfterDownload: nil, ClearExpiry: false}
	s.Require().NoError(s.be.files.Update(ctx, "edge", "object-edge", opts, before), "a live file takes the update")
	s.Require().ErrorIs(s.be.files.Update(ctx, "edge", "object-edge", opts, expires), domain.ErrNotFound,
		"an update at expires_at finds an expired file")

	s.Require().ErrorIs(s.be.files.DeleteExpired(ctx, "edge", "object-edge", before), domain.ErrNotFound)
	s.Require().NoError(s.be.files.DeleteExpired(ctx, "edge", "object-edge", expires))
}

func (s *SessionSuite) TestExpiryBoundary_ExpiredAtExpiresAt() {
	ctx := s.T().Context()
	usr := s.createUser("edge", false)
	expires := now()
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("edge", usr.ID, expires)))

	_, err := s.be.sessions.Get(ctx, "edge", expires.Add(-time.Second))
	s.Require().NoError(err, "live before expires_at")

	_, err = s.be.sessions.Get(ctx, "edge", expires)
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "expired at expires_at")

	s.Require().NoError(s.be.sessions.DeleteExpired(ctx, expires))
	s.Equal(int64(0), s.sessionRows(), "DeleteExpired removes the session that is expired at now")
}

func (s *APITokenSuite) TestExpiryBoundary_TokenExpiredAtCreatedAtDoesNotCount() {
	ctx := s.T().Context()
	usr := s.createUser("edge", false)
	expires := now()

	created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("edge", usr.ID, &expires), 1)
	s.Require().NoError(err)
	s.Require().True(created)

	// newAPIToken sets CreatedAt to now(), the expires_at of the first token.
	created, err = s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("next", usr.ID, nil), 1)
	s.Require().NoError(err)
	s.True(created, "the token is expired at its expires_at, so it does not count")
}

func (s *InviteSuite) TestExpiryBoundary_ExpiredAtExpiresAt() {
	ctx := s.T().Context()
	expires := now()
	s.createInvite("edge", expires)

	err := s.be.invites.RedeemInvite(ctx, "edge", "member", newUser("member", false), expires)
	s.Require().ErrorIs(err, domain.ErrInviteExpired, "expired at expires_at")

	s.Require().NoError(
		s.be.invites.RedeemInvite(ctx, "edge", "member", newUser("member", false), expires.Add(-time.Second)),
		"live before expires_at")
}
