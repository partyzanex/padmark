package links

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// readerLink returns a stored link with the given privacy, owner, expiry and burn setting.
func readerLink(privacy domain.Privacy, owner *uuid.UUID, expiresAt *time.Time, burn bool) *domain.Link {
	return &domain.Link{
		CreatedAt: time.Time{}, ExpiresAt: expiresAt, Privacy: &privacy, OwnerID: owner, DirectRedirect: nil,
		ID: "", TargetURL: "https://example.com", EditCode: "secret123456", Views: 0, BurnAfterClick: burn,
	}
}

// strangerReader is a signed-in reader that owns nothing, with a deliberate access.
func strangerReader() domain.Reader {
	return accountReader(uuid.New())
}

// TestFollow_PrivacyIsCheckedFirst checks that a hidden link gives ErrForbidden and that storage
// does not delete, consume or count it, also when it is expired or a burn link.
func (s *ManagerTestSuite) TestFollow_PrivacyIsCheckedFirst() {
	past := testNow().Add(-time.Minute)
	owner := uuid.New()
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("priv")).
		Return(readerLink(domain.PrivacyOwner, &owner, &past, true), nil)

	_, err := s.manager.Follow(s.T().Context(), "priv", strangerReader())

	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// TestFollow_AccountsOffSkipsPrivacy checks that SkipPrivacy follows an owner-only link and counts
// the view without the storage privacy check.
func (s *ManagerTestSuite) TestFollow_AccountsOffSkipsPrivacy() {
	owner := uuid.New()
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("off")).
		Return(readerLink(domain.PrivacyOwner, &owner, nil, false), nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("off")).Return(nil)

	link, err := s.manager.Follow(s.T().Context(), "off", openReader())

	s.Require().NoError(err)
	s.Equal(1, link.Views)
}

// TestFollow_VisibleCounterChecksReader checks that with privacy on, the counter goes through the
// storage privacy check of the current row, and that a refused counter stops the redirect.
func (s *ManagerTestSuite) TestFollow_VisibleCounterChecksReader() {
	reader := strangerReader()

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("auth")).
		Return(readerLink(domain.PrivacyAuthenticated, nil, nil, false), nil)
	s.storage.EXPECT().IncrementViewsVisible(gomock.Any(), domain.HashSlug("auth"), reader.CallerID, true).
		Return(domain.ErrNotFound)

	_, err := s.manager.Follow(s.T().Context(), "auth", reader)

	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestFollow_BurnNeedsIntent checks that a burn link stays without AllowBurn, and that with it
// the visible consume removes the link.
func (s *ManagerTestSuite) TestFollow_BurnNeedsIntent() {
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("burn")).
		Return(readerLink(domain.PrivacyPublic, nil, nil, true), nil)

	held := strangerReader()
	held.AllowBurn = false

	_, err := s.manager.Follow(s.T().Context(), "burn", held)
	s.Require().ErrorIs(err, domain.ErrBurnIntentRequired)

	reader := strangerReader()

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("burn")).
		Return(readerLink(domain.PrivacyPublic, nil, nil, true), nil)
	s.storage.EXPECT().ConsumeVisible(gomock.Any(), domain.HashSlug("burn"), reader.CallerID, true, true, testNow()).
		Return(readerLink(domain.PrivacyPublic, nil, nil, true), nil)

	link, err := s.manager.Follow(s.T().Context(), "burn", reader)
	s.Require().NoError(err)
	s.Equal("https://example.com", link.TargetURL)
}

// TestInspect_ExpiredIsKept checks that Inspect reports a visible expired link without a delete
// (the metadata read has no side effect), and never consumes a live burn link.
func (s *ManagerTestSuite) TestInspect_ExpiredIsKept() {
	past := testNow().Add(-time.Minute)
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("old")).
		Return(readerLink(domain.PrivacyPublic, nil, &past, false), nil)

	_, err := s.manager.Inspect(s.T().Context(), "old", openReader())
	s.Require().ErrorIs(err, domain.ErrExpired)

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("burn")).
		Return(readerLink(domain.PrivacyPublic, nil, nil, true), nil)

	link, err := s.manager.Inspect(s.T().Context(), "burn", openReader())
	s.Require().NoError(err)
	s.True(link.BurnAfterClick)
}

// TestFollow_ExpiredIsDeleted checks that Follow deletes a visible expired link.
func (s *ManagerTestSuite) TestFollow_ExpiredIsDeleted() {
	past := testNow().Add(-time.Minute)
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("old")).
		Return(readerLink(domain.PrivacyPublic, nil, &past, false), nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("old"), testNow()).Return(nil)

	_, err := s.manager.Follow(s.T().Context(), "old", openReader())
	s.Require().ErrorIs(err, domain.ErrExpired)
}

// TestDelete_HiddenLinkIsForbidden checks that the right edit code does not open a hidden link.
func (s *ManagerTestSuite) TestDelete_HiddenLinkIsForbidden() {
	owner := uuid.New()
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("hidden")).
		Return(readerLink(domain.PrivacyOwner, &owner, nil, false), nil)

	err := s.manager.Delete(s.T().Context(), "hidden", "secret123456", strangerReader())

	s.Require().ErrorIs(err, domain.ErrForbidden)
}
