package notes

import (
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestCreate_RejectsShortCustomEditCode pins finding M-8: a caller-chosen edit code shorter
// than domain.MinEditCodeLength is rejected before anything is resolved or persisted (no
// slug lookup, no storage call — the mocks would fail on an unexpected call).
func (s *ManagerTestSuite) TestCreate_RejectsShortCustomEditCode() {
	for _, code := range []string{"1", "1234567"} {
		note := &domain.Note{Title: "hello", Content: "world", EditCode: code}

		result, err := s.manager.Create(s.T().Context(), note)

		s.Require().ErrorIs(err, domain.ErrEditCodeTooShort, "code %q", code)
		s.Nil(result)
	}
}

// TestCreate_RejectsMalformedCustomEditCode covers the other half of the policy: surrounding
// whitespace would make the code unverifiable through the X-Edit-Code header.
func (s *ManagerTestSuite) TestCreate_RejectsMalformedCustomEditCode() {
	note := &domain.Note{Title: "hello", Content: "world", EditCode: " 12345678"}

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().ErrorIs(err, domain.ErrEditCodeMalformed)
}

// TestCreate_AcceptsMinLengthCustomEditCode is the boundary control: exactly
// domain.MinEditCodeLength characters is accepted.
func (s *ManagerTestSuite) TestCreate_AcceptsMinLengthCustomEditCode() {
	note := &domain.Note{Title: "hello", Content: "world", EditCode: "12345678"}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("12345678", result.EditCode)
}
