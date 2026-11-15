package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// headerUser returns a signed-in user for the header tests.
func headerUser(name string, admin bool) *domain.User {
	return &domain.User{
		ID: testUserID1, Username: name,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: admin,
	}
}

// getPage requests an HTML page; session, when set, is the value of the session cookie.
func getPage(router http.Handler, path, session string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html")

	if session != "" {
		req.AddCookie(sessionCookieValue(session))
	}

	router.ServeHTTP(rec, req)

	return rec
}

// TestHeader_AccountsOff_NoSignIn: without the account system nobody can sign in, so the header
// has no "Sign in" button and no account block.
func (s *HandlerSuite) TestHeader_AccountsOff_NoSignIn() {
	rec := getPage(s.router, "/", "")

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotContains(rec.Body.String(), "header-signin")
	s.NotContains(rec.Body.String(), "account-signout")
	s.Contains(rec.Body.String(), `class="nav-tab" aria-current="page"`)
}

// TestHeader_SignedOut_ShowsSignIn: with accounts on, an anonymous visitor gets "Sign in" on a
// page that needs no session (the create pages redirect such a visitor to the sign-in page).
func (s *AuthHandlerSuite) TestHeader_SignedOut_ShowsSignIn() {
	// An empty user store opens the first-admin setup page to an anonymous visitor.
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)

	rec := getPage(s.router, "/setup", "")

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), `<a href="/login" class="btn-ghost header-signin">Sign in</a>`)
	s.NotContains(rec.Body.String(), "account-signout")
}

// TestHeader_LoginPage_MarksSignIn: the sign-in page marks its own header button as current.
func (s *AuthHandlerSuite) TestHeader_LoginPage_MarksSignIn() {
	rec := getPage(s.router, "/login", "")

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), `class="btn-ghost header-signin" aria-current="page"`)
}

// TestHeader_SignedIn_ShowsAccount: a signed-in user gets the account block with the name, the
// role and a sign-out form that carries the CSRF token; one session lookup serves the page.
func (s *AuthHandlerSuite) TestHeader_SignedIn_ShowsAccount() {
	cases := []struct {
		name  string
		role  string
		link  string
		admin bool
	}{
		{name: "alice", role: "<span>User</span>", link: `href="/change-password" class="account-name"`, admin: false},
		{name: "root", role: "<span>Admin</span>", link: `href="/admin" class="account-name"`, admin: true},
	}

	for _, test := range cases {
		s.Run(test.name, func() {
			s.auth.EXPECT().GetSession(gomock.Any(), "sess").Return(headerUser(test.name, test.admin), nil)

			rec := getPage(s.router, "/", "sess")
			body := rec.Body.String()

			s.Require().Equal(http.StatusOK, rec.Code)
			s.Contains(body, "<strong>"+test.name+"</strong>")
			s.Contains(body, test.role)
			s.Contains(body, test.link)
			s.Contains(body, `aria-label="Sign out"`)
			s.Regexp(`<form method="POST" action="/logout">\s*<input type="hidden" name="csrf_token" value="[^"]+">`, body)
			s.NotContains(body, "header-signin")
		})
	}
}

// TestHeader_SessionLookupFails_ShowsSignIn: a failed session lookup counts as signed out
// (fail-soft): the page renders and offers "Sign in". The call count is not part of the contract:
// the sign-in page does its own session check too.
func (s *AuthHandlerSuite) TestHeader_SessionLookupFails_ShowsSignIn() {
	s.auth.EXPECT().GetSession(gomock.Any(), "stale").Return(nil, errors.New("session not found")).AnyTimes()

	rec := getPage(s.router, "/login", "stale")

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "header-signin")
	s.NotContains(rec.Body.String(), "account-signout")
}

// TestHeader_ReaderPage_HasNewButton: reader pages replace the create tabs with "New".
func (s *HandlerSuite) TestHeader_ReaderPage_HasNewButton() {
	rec := getPage(s.router, "/success?id=abc", "")

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), `class="btn-primary header-new"`)
	s.NotContains(rec.Body.String(), `class="nav-create`)
}
