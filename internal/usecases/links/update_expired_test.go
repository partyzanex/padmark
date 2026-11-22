package links

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestUpdate_ExpiredLinkIsGone: an expired link gets the error of a read, before the edit code
// check and also for its owner. Storage gets no write: the strict mock fails the test on a
// Storage.Update call.
func (s *ManagerTestSuite) TestUpdate_ExpiredLinkIsGone() {
	owner := uuid.New()

	for name, caller := range map[string]struct {
		code   string
		reader domain.Reader
	}{
		"correct code": {code: "original", reader: openReader()},
		"wrong code":   {code: "wrong", reader: openReader()},
		"owner":        {code: "", reader: accountReader(owner)},
	} {
		s.Run(name, func() {
			s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("stable")).
				Return(expiredLink(owner, nil), nil)

			_, err := s.manager.Update(s.T().Context(), "stable", caller.code, caller.reader,
				"https://example.com/new", clearExpiry())
			s.Require().ErrorIs(err, domain.ErrExpired)
		})
	}
}

// TestUpdate_HiddenExpiredLinkIsForbidden: privacy comes before expiry, so a reader that cannot
// see the link does not learn that it expired.
func (s *ManagerTestSuite) TestUpdate_HiddenExpiredLinkIsForbidden() {
	privacy := domain.PrivacyOwner
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("stable")).
		Return(expiredLink(uuid.New(), &privacy), nil)

	_, err := s.manager.Update(s.T().Context(), "stable", "original", accountReader(uuid.New()),
		"https://example.com/new", clearExpiry())
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// expiredLink is a stored link of owner that expired one minute ago.
func expiredLink(owner uuid.UUID, privacy *domain.Privacy) *domain.Link {
	past := testNow().Add(-time.Minute)

	return &domain.Link{
		OwnerID: &owner, EditCode: "original", ExpiresAt: &past, Privacy: privacy,
		CreatedAt: time.Time{}, DirectRedirect: nil, ID: domain.HashSlug("stable"), TargetURL: "old",
		Views: 0, BurnAfterClick: false,
	}
}

// clearExpiry is an update that removes the expiry: the change that an expired link must not get.
func clearExpiry() UpdateOptions {
	return UpdateOptions{
		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, ClearExpiry: true, ResetDirectRedirect: false,
	}
}
