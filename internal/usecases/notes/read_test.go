package notes

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// readerNote returns a stored note with the given privacy, owner, expiry and burn setting.
func readerNote(privacy domain.Privacy, owner *uuid.UUID, expiresAt *time.Time, burn bool) *domain.Note {
	return &domain.Note{
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: expiresAt,
		Privacy: &privacy, OwnerID: owner, ShowContents: nil,
		ID: "", Title: "t", Content: "body", ContentType: nil, EditCode: "secret123456",
		Views: 0, BurnTTL: 0, BurnAfterReading: burn, ClearExpiry: false,
	}
}

// strangerReader is a signed-in reader that owns nothing.
func strangerReader() domain.Reader {
	return domain.Reader{CallerID: testUserID2, Authenticated: true, AllowBurn: true, SkipPrivacy: false}
}

// TestRead_PrivacyIsCheckedFirst checks that a reader who cannot see the note gets ErrForbidden,
// and that the note is not deleted, burned or counted, also when it is expired.
func (s *ManagerTestSuite) TestRead_PrivacyIsCheckedFirst() {
	past := testNow().Add(-time.Minute)
	owner := testUserID1
	s.expectGet("priv", readerNote(domain.PrivacyOwner, &owner, &past, true))

	_, err := s.manager.Read(s.T().Context(), "priv", strangerReader())

	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// TestRead_AuthenticatedPrivacy checks the authenticated level for an anonymous and a signed-in reader.
func (s *ManagerTestSuite) TestRead_AuthenticatedPrivacy() {
	s.expectGet("auth", readerNote(domain.PrivacyAuthenticated, nil, nil, false))

	anonymous := domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: true, SkipPrivacy: false}
	_, err := s.manager.Read(s.T().Context(), "auth", anonymous)
	s.Require().ErrorIs(err, domain.ErrForbidden)

	s.expectGet("auth", readerNote(domain.PrivacyAuthenticated, nil, nil, false))
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("auth")).Return(nil)

	note, err := s.manager.Read(s.T().Context(), "auth", strangerReader())
	s.Require().NoError(err)
	s.Equal(1, note.Views)
}

// TestRead_AccountsOffSkipsPrivacy checks that SkipPrivacy (accounts off) shows an owner-only note.
func (s *ManagerTestSuite) TestRead_AccountsOffSkipsPrivacy() {
	owner := testUserID1
	s.expectGet("off", readerNote(domain.PrivacyOwner, &owner, nil, false))
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("off")).Return(nil)

	note, err := s.manager.Read(s.T().Context(), "off", openReader())

	s.Require().NoError(err)
	s.Equal("body", note.Content)
}

// TestRead_ExpiredIsDeleted checks that a visible expired note is deleted and gives ErrExpired,
// before the burn intent check.
func (s *ManagerTestSuite) TestRead_ExpiredIsDeleted() {
	past := testNow().Add(-time.Minute)
	s.expectGet("old", readerNote(domain.PrivacyPublic, nil, &past, true))
	s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("old"), testNow()).Return(nil)

	reader := openReader()
	reader.AllowBurn = false

	_, err := s.manager.Read(s.T().Context(), "old", reader)

	s.Require().ErrorIs(err, domain.ErrExpired)
}

// TestRead_BurnNeedsIntent checks that a burn note stays without AllowBurn: no Consume, no
// SetBurnExpiry and no view count.
func (s *ManagerTestSuite) TestRead_BurnNeedsIntent() {
	s.expectGet("burn", readerNote(domain.PrivacyPublic, nil, nil, true))

	reader := openReader()
	reader.AllowBurn = false

	_, err := s.manager.Read(s.T().Context(), "burn", reader)

	s.Require().ErrorIs(err, domain.ErrBurnIntentRequired)
}

// TestInspect_NeverBurns checks that Inspect returns a live burn note without a side effect,
// and applies privacy.
func (s *ManagerTestSuite) TestInspect_NeverBurns() {
	s.expectGet("burn", readerNote(domain.PrivacyPublic, nil, nil, true))

	note, err := s.manager.Inspect(s.T().Context(), "burn", openReader())

	s.Require().NoError(err)
	s.True(note.BurnAfterReading)

	owner := testUserID1
	s.expectGet("burn", readerNote(domain.PrivacyOwner, &owner, nil, true))

	_, err = s.manager.Inspect(s.T().Context(), "burn", strangerReader())
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// TestUpdate_HiddenNoteIsForbidden checks that the right edit code does not open a note that
// privacy hides: Update and Delete return ErrForbidden and write nothing.
func (s *ManagerTestSuite) TestUpdate_HiddenNoteIsForbidden() {
	owner := testUserID1
	s.expectGet("hidden", readerNote(domain.PrivacyOwner, &owner, nil, false))

	_, err := s.manager.Update(s.T().Context(), "hidden", "secret123456", strangerReader(), &domain.Note{
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		ShowContents: nil, ID: "", Title: "x", Content: "y", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	})
	s.Require().ErrorIs(err, domain.ErrForbidden)

	s.expectGet("hidden", readerNote(domain.PrivacyOwner, &owner, nil, false))

	err = s.manager.Delete(s.T().Context(), "hidden", "secret123456", strangerReader())
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// TestDelete_AccountsOffUsesEditCode checks that SkipPrivacy keeps the edit-code check.
func (s *ManagerTestSuite) TestDelete_AccountsOffUsesEditCode() {
	owner := testUserID1
	s.expectGet("off", readerNote(domain.PrivacyOwner, &owner, nil, false))

	err := s.manager.Delete(s.T().Context(), "off", "wrong-code00", openReader())

	s.Require().ErrorIs(err, domain.ErrInvalidEditCode)
}

// TestRead_OtherKindKeepsTheKind checks that a slug of a short link or a file gives the
// *domain.OtherKindError of the storage to the caller of Read, Inspect and ReadRendered, so that
// GET /{id} can call the Manager of that kind. The error still matches ErrNotFound.
func (s *ManagerTestSuite) TestRead_OtherKindKeepsTheKind() {
	ctx := s.T().Context()
	reads := map[string]func() error{
		"Read": func() error {
			_, err := s.manager.Read(ctx, "held", openReader())

			return err
		},
		"Inspect": func() error {
			_, err := s.manager.Inspect(ctx, "held", openReader())

			return err
		},
		"ReadRendered": func() error {
			_, _, err := s.manager.ReadRendered(ctx, "held", openReader())

			return err
		},
	}

	for name, read := range reads {
		s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("held")).
			Return(nil, &domain.OtherKindError{Kind: domain.KindFile})

		err := read()

		other, _ := errors.AsType[*domain.OtherKindError](err)
		s.Require().NotNil(other, name)
		s.Equal(domain.KindFile, other.Kind, name)
		s.Require().ErrorIs(err, domain.ErrNotFound, name)
	}
}
