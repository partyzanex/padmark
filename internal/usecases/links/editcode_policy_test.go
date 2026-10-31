package links

import (
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestCreate_RejectsShortCustomEditCode pins finding M-8 for links: a caller-chosen edit code
// shorter than domain.MinEditCodeLength is rejected before any slug lookup or storage call.
func (s *ManagerTestSuite) TestCreate_RejectsShortCustomEditCode() {
	for _, code := range []string{"1", "1234567"} {
		link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
			EditCode:  code,
			ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
			CustomHash: "", BurnAfterClick: false,
		})

		s.Require().ErrorIs(err, domain.ErrEditCodeTooShort, "code %q", code)
		s.Nil(link)
	}
}

// TestCreate_RejectsMalformedCustomEditCode: surrounding whitespace is rejected too.
func (s *ManagerTestSuite) TestCreate_RejectsMalformedCustomEditCode() {
	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		EditCode:  "12345678 ",
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", BurnAfterClick: false,
	})

	s.Require().ErrorIs(err, domain.ErrEditCodeMalformed)
}

// TestCreate_AcceptsMinLengthCustomEditCode is the boundary control: exactly
// domain.MinEditCodeLength characters is accepted.
func (s *ManagerTestSuite) TestCreate_AcceptsMinLengthCustomEditCode() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		EditCode:  "12345678",
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
	s.Equal("12345678", link.EditCode)
}
