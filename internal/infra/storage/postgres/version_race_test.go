//go:build integration

package postgres

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// These tests show the check-then-write race: a caller reads version 1 of a record and checks
// the owner or the edit code. Then version 1 is deleted and a new record (version 2) takes the
// same ID. A write that carries the stored edit-code hash of version 1 must return
// domain.ErrNotFound and must not change version 2.

const reusedID = "reused-id"

func noteVersion(title, editCode string, burn bool) *domain.Note {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &domain.Note{
		ID: reusedID, Title: title, Content: "content of " + title,
		ContentType: new(domain.ContentTypeMarkdown), CreatedAt: now, UpdatedAt: now,
		EditCode: editCode, BurnAfterReading: burn,

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, Views: 0, BurnTTL: 0,
		ClearExpiry: false, ShowContents: nil,
	}
}

// replaceNote creates version 1, reads it like a manager does, deletes it and creates version 2
// with the same ID. It returns the version 1 snapshot that the caller checked.
func (s *RepositoryTestSuite) replaceNote(second *domain.Note) *domain.Note {
	ctx := s.T().Context()

	s.Require().NoError(s.repo.Create(ctx, noteVersion("first", "s1$first-salt$first-key", false)))

	checked, err := s.repo.Get(ctx, reusedID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Delete(ctx, reusedID, checked.EditCode))
	s.Require().NoError(s.repo.Create(ctx, second))

	return checked
}

func (s *RepositoryTestSuite) TestStaleVersion_UpdateKeepsNewNote() {
	ctx := s.T().Context()
	checked := s.replaceNote(noteVersion("second", "s1$second-salt$second-key", false))

	stale := *checked
	stale.Title, stale.Content, stale.UpdatedAt = "overwritten", "overwritten", time.Now().UTC()

	s.Require().ErrorIs(s.repo.Update(ctx, reusedID, &stale, time.Now()), domain.ErrNotFound)

	got, err := s.repo.Get(ctx, reusedID)
	s.Require().NoError(err)
	s.Equal("second", got.Title)
	s.Equal("content of second", got.Content)
}

func (s *RepositoryTestSuite) TestStaleVersion_DeleteKeepsNewNote() {
	ctx := s.T().Context()
	second := noteVersion("second", "s1$second-salt$second-key", false)
	checked := s.replaceNote(second)

	s.Require().ErrorIs(s.repo.Delete(ctx, reusedID, checked.EditCode), domain.ErrNotFound)

	got, err := s.repo.Get(ctx, reusedID)
	s.Require().NoError(err)
	s.Equal("second", got.Title)

	s.Require().NoError(s.repo.Delete(ctx, reusedID, second.EditCode), "the current version deletes")
}

func (s *RepositoryTestSuite) TestStaleVersion_BurnKeepsNewNote() {
	ctx := s.T().Context()
	// Version 2 is a burn note with the same privacy, so only the version check refuses it.
	checked := s.replaceNote(noteVersion("second", "s1$second-salt$second-key", true))

	_, err := s.repo.Consume(ctx, reusedID, domain.PrivacyPublic, checked.EditCode, time.Now())
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = s.repo.SetBurnExpiry(ctx, reusedID, time.Now().Add(time.Hour), domain.PrivacyPublic, checked.EditCode)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	got, err := s.repo.Get(ctx, reusedID)
	s.Require().NoError(err)
	s.Equal("second", got.Title)
	s.True(got.BurnAfterReading)
	s.Nil(got.ExpiresAt)
}

func linkVersion(target, editCode string) *domain.Link {
	return &domain.Link{
		ID: reusedID, TargetURL: target, CreatedAt: time.Now().UTC(), EditCode: editCode,

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil, Views: 0, BurnAfterClick: false,
	}
}

// replaceLink is replaceNote for links.
func (s *LinkRepositoryTestSuite) replaceLink(second *domain.Link) *domain.Link {
	ctx := s.T().Context()

	s.Require().NoError(s.repo.Create(ctx, linkVersion("first target", "s1$first-salt$first-key")))

	checked, err := s.repo.GetByID(ctx, reusedID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Delete(ctx, reusedID, checked.EditCode))
	s.Require().NoError(s.repo.Create(ctx, second))

	return checked
}

func (s *LinkRepositoryTestSuite) TestStaleVersion_UpdateKeepsNewLink() {
	ctx := s.T().Context()
	checked := s.replaceLink(linkVersion("second target", "s1$second-salt$second-key"))

	_, err := s.repo.Update(ctx, reusedID, checked.EditCode, targetPatch("overwritten"), time.Now())
	s.Require().ErrorIs(err, domain.ErrNotFound)

	got, err := s.repo.GetByID(ctx, reusedID)
	s.Require().NoError(err)
	s.Equal("second target", got.TargetURL)
}

func (s *LinkRepositoryTestSuite) TestStaleVersion_DeleteKeepsNewLink() {
	ctx := s.T().Context()
	second := linkVersion("second target", "s1$second-salt$second-key")
	checked := s.replaceLink(second)

	s.Require().ErrorIs(s.repo.Delete(ctx, reusedID, checked.EditCode), domain.ErrNotFound)

	got, err := s.repo.GetByID(ctx, reusedID)
	s.Require().NoError(err)
	s.Equal("second target", got.TargetURL)

	s.Require().NoError(s.repo.Delete(ctx, reusedID, second.EditCode), "the current version deletes")
}
