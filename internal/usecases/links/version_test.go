package links

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// These tests check that a delete after an access or expiry check carries the stored edit-code
// hash of the checked link. Storage uses it to refuse a new link that took the ID after the check.

func storedLink(editCode string, expiresAt *time.Time) *domain.Link {
	return &domain.Link{
		TargetURL: "https://example.com", EditCode: editCode, ExpiresAt: expiresAt,

		CreatedAt: time.Time{}, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		ID: "", Views: 0, BurnAfterClick: false,
	}
}

func (s *ManagerTestSuite) TestDelete_UsesCheckedVersion() {
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(storedLink("secret123456", nil), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("the-slug"), "secret123456").Return(nil)

	s.Require().NoError(s.manager.Delete(s.T().Context(), "the-slug", "secret123456", uuid.Nil))
}

// TestDelete_NewVersionIsNotFound verifies that a refused stale delete reaches the caller as
// ErrNotFound, the answer for a deleted link.
func (s *ManagerTestSuite) TestDelete_NewVersionIsNotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(storedLink("secret123456", nil), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("the-slug"), "secret123456").Return(domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "the-slug", "secret123456", uuid.Nil)

	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestRedirect_ExpiredUsesConditionalDelete verifies that the expiry delete is the conditional
// DeleteExpired, never the unconditional Delete: storage checks the expiry again, so a new live
// link with the same ID stays.
func (s *ManagerTestSuite) TestRedirect_ExpiredUsesConditionalDelete() {
	past := time.Now().Add(-time.Hour)
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("expired-slug")).
		Return(storedLink("hash:expired", &past), nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("expired-slug")).Return(nil)

	_, err := s.manager.Redirect(s.T().Context(), "expired-slug")

	s.Require().ErrorIs(err, domain.ErrExpired)
}

// TestRedirect_ExtendedAfterExpiryCheckStays covers finding 8: the owner extends the link between
// the expiry decision and the DELETE. Storage refuses the DELETE because the row is not expired
// now. The manager must not fall back to an unconditional Delete: the strict mock fails on it.
func (s *ManagerTestSuite) TestRedirect_ExtendedAfterExpiryCheckStays() {
	past := time.Now().Add(-time.Hour)
	for _, redirect := range []func(ctx context.Context, slug string) (*domain.Link, error){
		s.manager.Redirect, s.manager.RedirectWithoutBurn,
	} {
		s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("extended")).
			Return(storedLink("hash:extended", &past), nil)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("extended")).Return(domain.ErrNotFound)

		_, err := redirect(s.T().Context(), "extended")
		s.Require().ErrorIs(err, domain.ErrExpired, "this request still saw the old expiry")
	}
}
