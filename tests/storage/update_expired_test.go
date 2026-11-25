//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestUpdate_ExpiredNoteIsNotChanged: the storage Update changes a note only while it is not
// expired, as the file Update does. A note that expired after the check of notes.Manager.Update
// gets domain.ErrNotFound, and the row keeps its content and its expiry.
func (s *NoteSuite) TestUpdate_ExpiredNoteIsNotChanged() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.notes.Create(ctx, newNote("expired", "edit-hash", &past)))

	update := newNote("expired", "edit-hash", nil)
	update.Title = "changed"

	s.Require().ErrorIs(s.be.notes.Update(ctx, "expired", update, now()), domain.ErrNotFound)

	got, err := s.be.notes.Get(ctx, "expired")
	s.Require().NoError(err)
	s.Equal("title", got.Title)
	s.Require().NotNil(got.ExpiresAt, "the update must not remove the expiry")
	s.True(past.Equal(*got.ExpiresAt))
}

// TestUpdate_ExpiredLinkIsNotChanged: the storage Update changes a link only while it is not
// expired, as the file Update does. The row keeps its target and its expiry.
func (s *LinkSuite) TestUpdate_ExpiredLinkIsNotChanged() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.links.Create(ctx, newLink("expired", &past)))

	_, err := s.be.links.Update(ctx, "expired", "edit-hash", &domain.LinkPatch{
		TargetURL: "new-ciphertext", ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ClearExpiry: true, ResetDirectRedirect: false,
	}, now())
	s.Require().ErrorIs(err, domain.ErrNotFound)

	got, err := s.be.links.GetByID(ctx, "expired")
	s.Require().NoError(err)
	s.Equal("ciphertext-url", got.TargetURL)
	s.Require().NotNil(got.ExpiresAt, "the update must not remove the expiry")
	s.True(past.Equal(*got.ExpiresAt))
}
