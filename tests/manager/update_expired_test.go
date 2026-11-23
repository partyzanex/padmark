//go:build integration

package integration

import (
	"bytes"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/files"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// The TestUpdate_Expired* tests check on each storage backend that an update of an expired note,
// link or file returns domain.ErrExpired with the correct edit code, and that the stored record
// keeps its expiry: the update must not extend the life of a record that readers see as gone.
// The tests read the stored row through the repository of the Manager, because a Manager read
// deletes an expired record or hides it. The expiry comes from the stored row, so the tests also
// cover the time values that each backend gives back.

func (s *ManagerSuite) TestUpdate_ExpiredNoteIsGone() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "t", Content: "c", ExpiresAt: &past,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil, ID: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: nil,
	})
	s.Require().NoError(err)

	_, err = s.Manager.Update(ctx, created.ID, created.EditCode, openReader(), &domain.Note{
		Title: "new", Content: "new", ClearExpiry: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		ID: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ShowContents: nil,
	})
	s.Require().ErrorIs(err, domain.ErrExpired)

	id := domain.HashSlug(created.ID)
	stored, err := s.NoteStorage.Get(ctx, id)
	s.Require().NoError(err)
	content, err := testutil.ContentEncryptor(s.T(), "note").Decrypt(stored.Content, id, created.ID)
	s.Require().NoError(err)
	s.Equal("c", content)
	s.Require().NotNil(stored.ExpiresAt)
	s.WithinDuration(past, *stored.ExpiresAt, time.Second)
}

func (s *ManagerSuite) TestUpdate_ExpiredLinkIsGone() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	created, err := s.LinkManager.Create(ctx, "https://example.com/old", links.CreateOptions{
		ExpiresAt: &past,
		Privacy:   nil, OwnerID: nil, DirectRedirect: nil, CustomHash: "", EditCode: "", BurnAfterClick: false,
	})
	s.Require().NoError(err)

	_, err = s.LinkManager.Update(ctx, created.ID, created.EditCode, openReader(), "https://example.com/new",
		links.UpdateOptions{
			ClearExpiry: true,
			ExpiresAt:   nil, Privacy: nil, DirectRedirect: nil, ResetDirectRedirect: false,
		})
	s.Require().ErrorIs(err, domain.ErrExpired)

	id := domain.HashSlug(created.ID)
	stored, err := s.LinkStorage.GetByID(ctx, id)
	s.Require().NoError(err)
	target, err := testutil.ContentEncryptor(s.T(), "link").Decrypt(stored.TargetURL, id, created.ID)
	s.Require().NoError(err)
	s.Equal("https://example.com/old", target)
	s.Require().NotNil(stored.ExpiresAt)
	s.WithinDuration(past, *stored.ExpiresAt, time.Second)
}

func (s *ManagerSuite) TestUpdate_ExpiredFileIsGone() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	created, err := s.FileManager.Create(ctx, "old.bin", bytes.NewReader(s.randomContent()), s.fileOptions(""))
	s.Require().NoError(err)

	_, err = s.DB.NewUpdate().Table("files").Set("expires_at = ?", past).
		Where("id = ?", domain.HashSlug(created.ID)).Exec(ctx)
	s.Require().NoError(err)

	_, err = s.FileManager.Update(ctx, created.ID, fileEditCode, openReader(), files.UpdateOptions{
		ClearExpiry: true, ExpiresAt: nil, Privacy: nil, BurnAfterDownload: nil,
	})
	s.Require().ErrorIs(err, domain.ErrExpired)

	stored, err := s.FileStorage.GetByID(ctx, domain.HashSlug(created.ID), time.Now())
	s.Require().NoError(err)
	s.Require().NotNil(stored.ExpiresAt)
	s.WithinDuration(past, *stored.ExpiresAt, time.Second)
}
