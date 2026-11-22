//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// LinkSuite is the contract of the link repository for Create, GetByID, Update, Delete and
// DeleteExpired. Consume, ConsumeVisible, IncrementViews and IncrementViewsVisible
// are not in it yet: another change refactors the link read paths now.
type LinkSuite struct {
	repoSuite
}

func (s *LinkSuite) TestCreate_GetByID_RoundTrip() {
	ctx := s.T().Context()
	expires := now().Add(time.Hour)
	link := newLink("link-1", &expires)
	link.DirectRedirect = new(true)
	link.BurnAfterClick = true

	s.Require().NoError(s.be.links.Create(ctx, link))

	got, err := s.be.links.GetByID(ctx, "link-1")
	s.Require().NoError(err)
	s.Equal("ciphertext-url", got.TargetURL)
	s.Equal("edit-hash", got.EditCode)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt))
	s.Require().NotNil(got.DirectRedirect)
	s.True(*got.DirectRedirect)
	s.True(got.BurnAfterClick)

	_, err = s.be.links.GetByID(ctx, "unknown")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkSuite) TestCreate_DuplicateSlug_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", nil)))

	s.Require().ErrorIs(s.be.links.Create(ctx, newLink("link-1", nil)), domain.ErrSlugConflict)
}

func (s *LinkSuite) TestCreate_SlugOfNote_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("shared", "edit-hash", nil)))

	s.Require().ErrorIs(s.be.links.Create(ctx, newLink("shared", nil)), domain.ErrSlugConflict)
}

func (s *LinkSuite) TestUpdate_WritesOnlyThePatch() {
	ctx := s.T().Context()
	expires := now().Add(time.Hour)
	link := newLink("link-1", &expires)
	link.Privacy = new(domain.PrivacyAuthenticated)
	s.Require().NoError(s.be.links.Create(ctx, link))

	patch := &domain.LinkPatch{
		TargetURL: "new-ciphertext", ExpiresAt: nil, Privacy: nil, DirectRedirect: new(false),
		ClearExpiry: false, ResetDirectRedirect: false,
	}

	_, err := s.be.links.Update(ctx, "link-1", "wrong-hash", patch, now())
	s.Require().ErrorIs(err, domain.ErrNotFound, "a different version is not changed")

	got, err := s.be.links.Update(ctx, "link-1", "edit-hash", patch, now())
	s.Require().NoError(err)
	s.Equal("new-ciphertext", got.TargetURL)
	s.Require().NotNil(got.ExpiresAt, "an omitted expiry stays")
	s.True(expires.Equal(*got.ExpiresAt))
	s.Equal(domain.PrivacyAuthenticated, got.EffectivePrivacy(), "an omitted privacy stays")
	s.Require().NotNil(got.DirectRedirect)
	s.False(*got.DirectRedirect)

	patch.ClearExpiry = true
	patch.ResetDirectRedirect = true

	got, err = s.be.links.Update(ctx, "link-1", "edit-hash", patch, now())
	s.Require().NoError(err)
	s.Nil(got.ExpiresAt)
	s.Nil(got.DirectRedirect)
}

func (s *LinkSuite) TestDelete_ChecksEditCode() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", nil)))

	s.Require().ErrorIs(s.be.links.Delete(ctx, "link-1", "wrong-hash"), domain.ErrNotFound)
	s.Require().NoError(s.be.links.Delete(ctx, "link-1", "edit-hash"))
	s.Require().ErrorIs(s.be.links.Delete(ctx, "link-1", "edit-hash"), domain.ErrNotFound)
}

func (s *LinkSuite) TestDeleteExpired_RemovesOnlyExpired() {
	ctx := s.T().Context()
	future := now().Add(time.Hour)
	s.Require().NoError(s.be.links.Create(ctx, newLink("live", &future)))
	s.Require().NoError(s.be.links.Create(ctx, newLink("forever", nil)))

	// Create removes old expired links of other slugs, so the expired link is the last insert.
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.links.Create(ctx, newLink("expired", &past)))

	s.Require().NoError(s.be.links.DeleteExpired(ctx, "expired", now()))
	s.Require().ErrorIs(s.be.links.DeleteExpired(ctx, "live", now()), domain.ErrNotFound)
	s.Require().ErrorIs(s.be.links.DeleteExpired(ctx, "forever", now()), domain.ErrNotFound)
}
