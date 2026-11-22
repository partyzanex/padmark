//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// NoteSuite is the contract of the note repository for Create, Get, Update, Delete,
// DeleteExpired and Exists. Consume, SetBurnExpiry and IncrementViews are not in it yet:
// another change refactors the note read paths now.
type NoteSuite struct {
	repoSuite
}

func (s *NoteSuite) TestCreate_Get_RoundTrip() {
	ctx := s.T().Context()
	expires := now().Add(time.Hour)
	note := newNote("note-1", "edit-hash", &expires)
	note.ShowContents = new(true)

	s.Require().NoError(s.be.notes.Create(ctx, note))

	got, err := s.be.notes.Get(ctx, "note-1")
	s.Require().NoError(err)
	s.Equal("title", got.Title)
	s.Equal("ciphertext", got.Content)
	s.Equal("edit-hash", got.EditCode)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt))
	s.Equal(domain.PrivacyPublic, got.EffectivePrivacy())
	s.True(got.ContentsSidebar())

	_, err = s.be.notes.Get(ctx, "unknown")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *NoteSuite) TestCreate_DuplicateSlug_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", nil)))

	s.Require().ErrorIs(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", nil)), domain.ErrSlugConflict)
}

// TestCreate_SlugOfLink_ReturnsErrSlugConflict pins the slug namespace: a note cannot take the
// slug of a link. On SQLite a trigger refuses it; on PostgreSQL slug_namespace_pkey does.
func (s *NoteSuite) TestCreate_SlugOfLink_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.links.Create(ctx, newLink("shared", nil)))

	s.Require().ErrorIs(s.be.notes.Create(ctx, newNote("shared", "edit-hash", nil)), domain.ErrSlugConflict)
}

func (s *NoteSuite) TestUpdate_ChecksEditCodeAndKeepsUnsetFields() {
	ctx := s.T().Context()
	note := newNote("note-1", "edit-hash", nil)
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.Require().NoError(s.be.notes.Create(ctx, note))

	update := newNote("note-1", "wrong-hash", nil)
	update.Title = "changed"
	update.ContentType = nil
	update.Privacy = nil
	s.Require().ErrorIs(s.be.notes.Update(ctx, "note-1", update), domain.ErrNotFound, "a different version is not changed")

	update.EditCode = "edit-hash"
	s.Require().NoError(s.be.notes.Update(ctx, "note-1", update))

	got, err := s.be.notes.Get(ctx, "note-1")
	s.Require().NoError(err)
	s.Equal("changed", got.Title)
	s.Equal(domain.PrivacyAuthenticated, got.EffectivePrivacy(), "a nil privacy keeps the stored value")
	s.Require().NotNil(got.ContentType)
	s.Equal(domain.ContentTypeMarkdown, *got.ContentType, "a nil content type keeps the stored value")
}

func (s *NoteSuite) TestDelete_ChecksEditCode() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", nil)))

	s.Require().ErrorIs(s.be.notes.Delete(ctx, "note-1", "wrong-hash"), domain.ErrNotFound)
	s.Require().NoError(s.be.notes.Delete(ctx, "note-1", "edit-hash"))
	s.Require().ErrorIs(s.be.notes.Delete(ctx, "note-1", "edit-hash"), domain.ErrNotFound)

	_, err := s.be.notes.Get(ctx, "note-1")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *NoteSuite) TestDeleteExpired_RemovesOnlyExpired() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	future := now().Add(time.Hour)

	s.Require().NoError(s.be.notes.Create(ctx, newNote("expired", "edit-hash", &past)))
	s.Require().NoError(s.be.notes.Create(ctx, newNote("live", "edit-hash", &future)))
	s.Require().NoError(s.be.notes.Create(ctx, newNote("forever", "edit-hash", nil)))

	s.Require().NoError(s.be.notes.DeleteExpired(ctx, "expired"))
	s.Require().ErrorIs(s.be.notes.DeleteExpired(ctx, "live"), domain.ErrNotFound)
	s.Require().ErrorIs(s.be.notes.DeleteExpired(ctx, "forever"), domain.ErrNotFound)
	s.Require().ErrorIs(s.be.notes.DeleteExpired(ctx, "unknown"), domain.ErrNotFound)
}

// TestExists_ReleasesExpiredSlug pins that an expired note does not hold its slug: Exists
// deletes it, and the slug is free for a link.
func (s *NoteSuite) TestExists_ReleasesExpiredSlug() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)

	s.Require().NoError(s.be.notes.Create(ctx, newNote("live", "edit-hash", nil)))
	s.Require().NoError(s.be.notes.Create(ctx, newNote("expired", "edit-hash", &past)))

	exists, err := s.be.notes.Exists(ctx, "live")
	s.Require().NoError(err)
	s.True(exists)

	exists, err = s.be.notes.Exists(ctx, "expired")
	s.Require().NoError(err)
	s.False(exists)

	s.Require().NoError(s.be.links.Create(ctx, newLink("expired", nil)), "the slug is free")
}
