//go:build integration

package storage

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// SlugSuite is the contract of the slug namespace read (slugs.Storage). The storage triggers
// write the rows, so each case creates the records through the note, link and file repositories.
type SlugSuite struct {
	repoSuite
}

func (s *SlugSuite) TestKind_ReturnsKindOfEachRecord() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", nil)))
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", nil)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	for id, want := range map[string]slugs.Kind{
		"note-1": slugs.KindNote, "link-1": slugs.KindLink, "file-1": slugs.KindFile,
	} {
		kind, err := s.be.slugs.Kind(ctx, id)
		s.Require().NoError(err, id)
		s.Equal(want, kind, id)
	}
}

func (s *SlugSuite) TestKind_MissingID_ReturnsErrNotFound() {
	_, err := s.be.slugs.Kind(s.T().Context(), "unknown")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestKind_ExpiredRecordKeepsRow checks that an expired record keeps its row until a delete:
// the web adapter then still selects the Manager, which answers 410.
func (s *SlugSuite) TestKind_ExpiredRecordKeepsRow() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", &past)))
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", &past)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", &past)))

	for _, id := range []string{"note-1", "link-1", "file-1"} {
		_, err := s.be.slugs.Kind(ctx, id)
		s.Require().NoError(err, id)
	}
}

// TestKind_DeletedRecordReleasesRow checks that the delete of each kind removes its row.
func (s *SlugSuite) TestKind_DeletedRecordReleasesRow() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", &past)))
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", &past)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	s.Require().NoError(s.be.notes.DeleteExpired(ctx, "note-1"))
	s.Require().NoError(s.be.links.DeleteExpired(ctx, "link-1"))
	s.Require().NoError(s.be.files.Delete(ctx, "file-1", "object-1"))

	for _, id := range []string{"note-1", "link-1", "file-1"} {
		_, err := s.be.slugs.Kind(ctx, id)
		s.Require().ErrorIs(err, domain.ErrNotFound, id)
	}
}
