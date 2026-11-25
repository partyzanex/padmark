//go:build integration

package storage

import (
	"errors"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// The cases below are the contract of the second answer of the note Get: when no note has the
// ID, the same query gives the kind of the record that has it, from the slug_namespace row that
// the storage triggers write. GET /{id} then calls the Manager of that kind without one more read.

func (s *NoteSuite) TestGet_SlugOfLinkOrFile_ReturnsOtherKindError() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", nil)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	for id, want := range map[string]domain.RecordKind{"link-1": domain.KindLink, "file-1": domain.KindFile} {
		note, err := s.be.notes.Get(ctx, id)

		s.Nil(note, id)
		s.Equal(want, otherKind(err), id)
		s.Require().ErrorIs(err, domain.ErrNotFound, id)
	}
}

func (s *NoteSuite) TestGet_NoteSlug_ReturnsNote() {
	ctx := s.T().Context()
	s.Require().NoError(s.be.notes.Create(ctx, newNote("note-1", "edit-hash", nil)))

	note, err := s.be.notes.Get(ctx, "note-1")

	s.Require().NoError(err)
	s.Equal("note-1", note.ID)
	s.Equal("ciphertext", note.Content)
}

func (s *NoteSuite) TestGet_MissingSlug_ReturnsOnlyErrNotFound() {
	_, err := s.be.notes.Get(s.T().Context(), "unknown")

	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Empty(otherKind(err), "no record has the ID, so the error tells no kind")
}

// TestGet_ExpiredRecordKeepsKind checks that an expired link or file keeps its namespace row
// until a delete: the web adapter then still calls its Manager, which answers 410.
func (s *NoteSuite) TestGet_ExpiredRecordKeepsKind() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", &past)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", &past)))

	for id, want := range map[string]domain.RecordKind{"link-1": domain.KindLink, "file-1": domain.KindFile} {
		_, err := s.be.notes.Get(ctx, id)

		s.Equal(want, otherKind(err), id)
	}
}

// TestGet_DeletedRecordReleasesKind checks that the delete of a link or a file removes its
// namespace row, so the note Get then tells no kind.
func (s *NoteSuite) TestGet_DeletedRecordReleasesKind() {
	ctx := s.T().Context()
	past := now().Add(-time.Minute)
	s.Require().NoError(s.be.links.Create(ctx, newLink("link-1", &past)))
	s.Require().NoError(s.be.files.Create(ctx, newFile("file-1", "object-1", nil)))

	s.Require().NoError(s.be.links.DeleteExpired(ctx, "link-1", now()))
	s.Require().NoError(s.be.files.Delete(ctx, "file-1", "object-1"))

	for _, id := range []string{"link-1", "file-1"} {
		_, err := s.be.notes.Get(ctx, id)

		s.Require().ErrorIs(err, domain.ErrNotFound, id)
		s.Empty(otherKind(err), id)
	}
}

// TestGet_LegacyNote_ReadWithoutNamespaceRow checks that a v0.1.6 note, which has no namespace
// row until encryption migrate converts it, is still found: the note join does not depend on
// the namespace row.
func (s *NoteSuite) TestGet_LegacyNote_ReadWithoutNamespaceRow() {
	ctx := s.T().Context()
	_, err := s.be.db.NewRaw(`INSERT INTO notes (id, title, content, created_at, updated_at, legacy_slug)
		VALUES ('legacy-1', 'note', 'plaintext', ?, ?, 'legacy-1')`, now(), now()).Exec(ctx)
	s.Require().NoError(err)

	rows, err := s.be.db.NewSelect().Table("slug_namespace").Where("id = ?", "legacy-1").Count(ctx)
	s.Require().NoError(err)
	s.Require().Zero(rows, "a legacy note has no namespace row")

	note, err := s.be.notes.Get(ctx, "legacy-1")

	s.Require().NoError(err)
	s.Equal("plaintext", note.Content)
}

// --- helpers ---

// otherKind returns the kind of the *domain.OtherKindError in err, or "" when err has none.
func otherKind(err error) domain.RecordKind {
	other, _ := errors.AsType[*domain.OtherKindError](err)
	if other == nil {
		return ""
	}

	return other.Kind
}
