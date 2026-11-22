//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// FileSuite is the contract of the file repository for Create, GetByID, Delete,
// ListExpired and DeleteExpired. ClaimBurn, ReleaseBurn, IncrementDownloadsVisible and Update
// are not in it yet: another change refactors the file read paths now.
type FileSuite struct {
	repoSuite
}

func (s *FileSuite) TestCreate_GetByID_RoundTrip() {
	ctx := s.T().Context()
	expires := now().Add(time.Hour)
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", &expires)))

	got, err := s.be.files.GetByID(ctx, "file-1")
	s.Require().NoError(err)
	s.Equal("object-1", got.ObjectKey)
	s.Equal("sealed-meta", got.SealedMeta)
	s.Equal("file-edit-hash", got.EditCode)
	s.Equal(int64(42), got.Size)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt))

	_, err = s.be.files.GetByID(ctx, "unknown")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *FileSuite) TestCreate_DuplicateSlug_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	s.Require().ErrorIs(s.be.files.Create(ctx, newFile("file-1", "object-2", nil)), domain.ErrSlugConflict)
}

func (s *FileSuite) TestCreate_SlugOfNoteOrLink_ReturnsErrSlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-slug", "edit-hash", nil)))
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-slug", nil)))

	s.Require().ErrorIs(s.be.files.Create(ctx, newFile("note-slug", "object-1", nil)), domain.ErrSlugConflict)
	s.Require().ErrorIs(s.be.files.Create(ctx, newFile("link-slug", "object-2", nil)), domain.ErrSlugConflict)
}

// TestCreate_DuplicateObjectKey_IsNotASlugConflict pins the S4 conflict mapping: only a slug
// conflict is ErrSlugConflict. A duplicate object key is an internal error, so the manager does
// not retry it as a slug collision.
func (s *FileSuite) TestCreate_DuplicateObjectKey_IsNotASlugConflict() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	err := s.be.files.Create(ctx, newFile("file-2", "object-1", nil))
	s.Require().Error(err)
	s.NotErrorIs(err, domain.ErrSlugConflict)
}

func (s *FileSuite) TestDelete() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	s.Require().ErrorIs(s.be.files.Delete(ctx, "file-1", "other-object"), domain.ErrNotFound)

	_, err := s.be.files.GetByID(ctx, "file-1")
	s.Require().NoError(err, "a delete with another object key keeps the record")

	s.Require().NoError(s.be.files.Delete(ctx, "file-1", "object-1"))

	_, err = s.be.files.GetByID(ctx, "file-1")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestListExpired_PagesByCursor pins the order (expiry, then ID) and the cursor of aba5c3a on
// both dialects.
func (s *FileSuite) TestListExpired_PagesByCursor() {
	ctx := s.T().Context()
	instant := now()
	older := instant.Add(-2 * time.Hour)
	newer := instant.Add(-time.Hour)
	future := instant.Add(time.Hour)

	s.Require().NoError(s.be.files.Create(ctx, newFile("b", "object-b", &older)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("a", "object-a", &older)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("c", "object-c", &newer)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("live", "object-live", &future)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("forever", "object-forever", nil)))

	first, err := s.be.files.ListExpired(ctx, instant, files.ExpiredCursor{ExpiresAt: time.Time{}, ID: ""}, 2)
	s.Require().NoError(err)
	s.Equal([]string{"a", "b"}, fileIDs(first))

	last := first[len(first)-1]
	cursor := files.ExpiredCursor{ExpiresAt: *last.ExpiresAt, ID: last.ID}

	second, err := s.be.files.ListExpired(ctx, instant, cursor, 2)
	s.Require().NoError(err)
	s.Equal([]string{"c"}, fileIDs(second))
}

func (s *FileSuite) TestDeleteExpired_UsesNow() {
	ctx := s.T().Context()
	instant := now()
	expires := instant.Add(time.Minute)
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", &expires)))

	err := s.be.files.DeleteExpired(ctx, "file-1", "object-1", instant)
	s.Require().ErrorIs(err, domain.ErrNotFound, "not expired yet")
	s.Require().NoError(s.be.files.DeleteExpired(ctx, "file-1", "object-1", expires.Add(time.Second)))
}

// --- helpers ---

func fileIDs(list []*domain.File) []string {
	ids := make([]string, 0, len(list))
	for _, file := range list {
		ids = append(ids, file.ID)
	}

	return ids
}
