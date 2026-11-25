package notes

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestUpdate_ExpiredNoteIsGone: an expired note (a hard TTL or a started burn timer) gets the
// error of a read, before the edit code check and also for its owner. The note stays for the
// read path, and storage gets no write: the strict mock fails the test on a Storage.Update call.
func (s *ManagerTestSuite) TestUpdate_ExpiredNoteIsGone() {
	for name, reader := range map[string]struct {
		code   string
		reader domain.Reader
	}{
		"correct code": {code: "code", reader: openReader()},
		"wrong code":   {code: "wrong", reader: openReader()},
		"owner":        {code: "", reader: accountReader(testUserID1)},
	} {
		s.Run(name, func() {
			past := testNow().Add(-time.Minute)
			s.expectGet("old-note", &domain.Note{
				ID: domain.HashSlug("old-note"), Content: "c", EditCode: "code", ExpiresAt: &past,
				OwnerID: &testUserID1, Title: "", CreatedAt: time.Time{}, UpdatedAt: time.Time{},
				Privacy: nil, ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
				ClearExpiry: false, ShowContents: nil,
			})

			_, err := s.manager.Update(s.T().Context(), "old-note", reader.code, reader.reader, updateBody())
			s.Require().ErrorIs(err, domain.ErrExpired)
		})
	}
}

// TestUpdate_HiddenExpiredNoteIsForbidden: privacy comes before expiry, so a reader that cannot
// see the note does not learn that it expired.
func (s *ManagerTestSuite) TestUpdate_HiddenExpiredNoteIsForbidden() {
	past := testNow().Add(-time.Minute)
	privacy := domain.PrivacyOwner
	s.expectGet("old-note", &domain.Note{
		ID: domain.HashSlug("old-note"), Content: "c", EditCode: "code", ExpiresAt: &past,
		OwnerID: &testUserID1, Privacy: &privacy, Title: "", CreatedAt: time.Time{}, UpdatedAt: time.Time{},
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	})

	_, err := s.manager.Update(s.T().Context(), "old-note", "code", strangerReader(), updateBody())
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

// updateBody is a valid update that removes the expiry: the change that an expired note must not get.
func updateBody() *domain.Note {
	return &domain.Note{
		Title: "new", Content: "new content", ClearExpiry: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		ID: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false, ShowContents: nil,
	}
}
