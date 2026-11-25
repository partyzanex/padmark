package web_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// --- access with accounts (R-59) ---

// TestPrivateFile_ReadDenials: each read path refuses a caller that cannot see the file. A
// browser goes to the sign-in page, or gets 403 when it is signed in; an API client gets 401.
func (s *FileHandlerSuite) TestPrivateFile_ReadDenials() {
	router := s.newAuthRouter(s.auth)

	s.Run("file page, anonymous browser", func() {
		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).Return(nil, domain.ErrForbidden)

		rec := s.serveWith(router, browserGet("/abc"))

		s.Equal(http.StatusSeeOther, rec.Code)
		s.Equal("/login?next=%2Fabc", rec.Header().Get("Location"))
	})

	s.Run("file page, other user", func() {
		other := testUser(uuid.New())
		s.auth.EXPECT().GetSession(gomock.Any(), "other-session").Return(other, nil)
		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", domain.Reader{
			CallerID: other.ID, Authenticated: true, AllowBurn: false, SkipPrivacy: false,
		}).Return(nil, domain.ErrForbidden)

		req := browserGet("/abc")
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "other-session"))
		rec := s.serveWith(router, req)

		s.Equal(http.StatusForbidden, rec.Code)
		s.NotContains(rec.Body.String(), "secret-name.txt")
	})

	s.Run("metadata", func() {
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).Return(nil, domain.ErrForbidden)

		s.Equal(http.StatusUnauthorized, s.serveWith(router, httptest.NewRequest(http.MethodGet, "/files/abc", nil)).Code)
	})

	s.Run("download", func() {
		s.files.EXPECT().Download(gomock.Any(), "abc", accountAccess(true)).Return(nil, domain.ErrForbidden)

		req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
		req.Header.Set("Accept", "application/octet-stream")

		s.Equal(http.StatusUnauthorized, s.serveWith(router, req).Code)
	})
}

// TestDelete_InvisibleFileIsUnauthorized: with accounts on, a caller that cannot see a private
// file gets 401: the usecase checks privacy before the edit code and returns ErrForbidden.
func (s *FileHandlerSuite) TestDelete_InvisibleFileIsUnauthorized() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "code-1234", anonymousReader()).Return(domain.ErrForbidden)

	req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "code-1234")

	s.Equal(http.StatusUnauthorized, s.serveWith(s.newAuthRouter(s.auth), req).Code)
}

// TestDelete_FailedCodesLockOut: wrong edit codes lock the slug, as for PUT.
func (s *FileHandlerSuite) TestDelete_FailedCodesLockOut() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "wrong", access(false)).Return(domain.ErrInvalidEditCode).Times(10)

	deleteRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
		req.Header.Set("X-Edit-Code", "wrong")

		return req
	}

	for range 10 {
		s.Equal(http.StatusForbidden, s.serve(deleteRequest()).Code)
	}

	s.Equal(http.StatusTooManyRequests, s.serve(deleteRequest()).Code)
}
