package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// ── Admin page ──.

func (s *AuthHandlerSuite) TestAdminPage_NoSession_Returns303() {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Sec-Fetch-Dest", "document") // simulate browser navigation.

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	// no session and no API token → the auth middleware blocks → redirect to login.
	s.Equal(http.StatusSeeOther, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminPage_NonAdmin_Returns403() {
	nonAdmin := &domain.User{
		ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	// The refusal is the HTML error page, not a bare text line.
	s.Contains(rec.Header().Get("Content-Type"), "text/html")
	s.Contains(rec.Body.String(), `<h1 class="error-title" id="errorTitle">Forbidden</h1>`)
}

func (s *AuthHandlerSuite) TestAdminPage_Admin_ListsUsers() {
	usr := s.adminUser()
	users := []*domain.User{usr, {
		ID: testUserID2, Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return(users, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "bob")
}

func (s *AuthHandlerSuite) TestAdminPage_Admin_ListsAPIKeys() {
	usr := s.adminUser()
	tokens := []*auth.APITokenInfo{
		{
			ID: "abcdef0123456789deadbeef", UserID: testAdminID, Username: "admin", CreatedAt: time.Now(),
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		},
	}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return(tokens, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "abcdef012345", "token hash prefix (first 12 chars) is shown")
	s.Contains(rec.Body.String(), "Never used", "unused token renders 'Never used'")
}

// ── Admin invite ──.

func (s *AuthHandlerSuite) TestAdminInvite_GeneratesLink() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "tok-xyz")
}

// adminInviteRequest builds a POST /admin/invite carrying admin-sess and a CSRF cookie/field pair
// minted for csrfSession ("" = the anonymous fixture), plus optional extra headers.
func (s *AuthHandlerSuite) adminInviteRequest(csrfSession string, headers map[string]string) *http.Request {
	token := testCSRFToken
	if csrfSession != "" {
		token = csrfTokenFor(csrfSession)
	}

	form := url.Values{"csrf_token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", token))

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return req
}

// TestAdminInvite_CSRFTokenFromBeforeSignInIsRejected — a cookie/field pair minted for an
// anonymous visitor (what a cookie-tossing attacker can produce) must not authorise a POST made
// with a real session cookie.
func (s *AuthHandlerSuite) TestAdminInvite_CSRFTokenFromBeforeSignInIsRejected() {
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(s.adminUser(), nil).AnyTimes()

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, s.adminInviteRequest("", nil))

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestAdminInvite_CrossSiteOriginIsRejected — a valid session-bound pair is still refused when
// the browser says the request came from another site; the same pair passes same-origin.
func (s *AuthHandlerSuite) TestAdminInvite_CrossSiteOriginIsRejected() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).AnyTimes()
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil).Times(1)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil).Times(1)

	for name, headers := range map[string]map[string]string{
		"foreign Origin":       {"Origin": "http://evil.test"},
		"cross-site fetch":     {"Sec-Fetch-Site": "cross-site"},
		"sibling-site fetch":   {"Sec-Fetch-Site": "same-site"},
		"opaque (null) Origin": {"Origin": "null"},
	} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, s.adminInviteRequest("admin-sess", headers))
		s.Equal(http.StatusForbidden, rec.Code, name)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, s.adminInviteRequest("admin-sess",
		map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}))
	s.Equal(http.StatusOK, rec.Code, "same-origin request with a session-bound token must pass")
}

func (s *AuthHandlerSuite) TestAdminInvite_ForcedSchemeEmbedsHTTPS() {
	// Behind a TLS-terminating proxy the request arrives as plain HTTP, but --public-scheme=https
	// is forced, so the invite link the admin copies must use https regardless — otherwise the
	// invitee is sent to an http:// setup URL that the proxy may not even serve.
	s.router = s.newRouterWithAuthAndScheme("https")

	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	// httptest.NewRequest defaults r.Host to example.com; the link is scheme://host/setup?invite=<token>.
	s.Contains(rec.Body.String(), "https://example.com/setup?invite=tok-xyz")
	s.NotContains(rec.Body.String(), "http://example.com/setup")
}

func (s *AuthHandlerSuite) TestAdminInvite_GenerateInviteFails_ShowsError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("", errors.New("boom"))
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to generate invite link")
}

func (s *AuthHandlerSuite) TestAdminInvite_AdminDataFails_Returns500() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return(nil, errors.New("boom"))

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusInternalServerError, rec.Code)
}

// ── Admin revoke ──.

func (s *AuthHandlerSuite) TestAdminRevoke_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevoke_NonAdmin_Returns403() {
	nonAdmin := &domain.User{
		ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevoke_Forbidden_RedirectsWithSpecificMessage() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(domain.ErrForbidden)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	s.Contains(loc, "revoke_error=")
	s.Contains(loc, url.QueryEscape("self-revoke or last admin"))
}

func (s *AuthHandlerSuite) TestAdminRevoke_GenericError_RedirectsWithGenericMessage() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(errors.New("boom"))

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	s.Contains(loc, "revoke_error=")
	s.Contains(loc, url.QueryEscape("Failed to revoke user."))
}

// ── Admin API keys ──.

// TestAdminCreateKey_Lifetime: the admin form's lifetime reaches the usecase as a session issue;
// an unparsable value is passed on as invalid and the refusal is shown on the page.
func (s *AuthHandlerSuite) TestAdminCreateKey_Lifetime() {
	usr := s.adminUser()

	for _, testCase := range []struct {
		form string
		ttl  int
		err  error
	}{
		{"90", 90, nil},
		{"", 0, nil},
		{"abc", -1, domain.ErrInvalidAPITokenTTL},
	} {
		s.Run("ttl_days="+testCase.form, func() {
			s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil)
			s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, auth.Issuer{
				APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
			}, testCase.ttl).
				Return("plain-secret-key", testCase.err)
			s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
			s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

			form := withCSRFFor(url.Values{"ttl_days": {testCase.form}}, "admin-sess")
			req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(s.sessionCookie("admin-sess"))
			req.AddCookie(s.csrfCookieFor("admin-sess"))

			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			s.Equal(http.StatusOK, rec.Code)

			if testCase.err != nil {
				s.Contains(rec.Body.String(), "Lifetime must be 0 (never expires) to 3650 days.")
			}
		})
	}
}

func (s *AuthHandlerSuite) TestAdminCreateKey_CreatesAndShowsKey() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)

	// The page shows a self-contained envelope (base URL + key), not the bare key: the raw key is
	// base64-packed inside it, so it must not leak verbatim, and the envelope must decode back to
	// the key plus the request's own URL (httptest defaults r.Host to example.com over http).
	envelope, err := domain.EncodeAPITokenEnvelope("http://example.com", "plain-secret-key")
	s.Require().NoError(err)
	s.Contains(rec.Body.String(), envelope, "envelope token is shown exactly once on the page")
	s.NotContains(rec.Body.String(), ">plain-secret-key<", "raw key must not appear outside the envelope")

	gotURL, gotKey, ok := domain.DecodeAPITokenEnvelope(envelope)
	s.True(ok)
	s.Equal("http://example.com", gotURL)
	s.Equal("plain-secret-key", gotKey)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_ForcedSchemeEmbedsHTTPS() {
	// Behind a TLS-terminating proxy the request itself still arrives as plain HTTP, but the
	// operator has forced --public-scheme=https, so the envelope embedded in the page must use
	// https regardless — otherwise the CLI decodes an http:// URL from a key meant for https.
	s.router = s.newRouterWithAuthAndScheme("https")

	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)

	envelope, err := domain.EncodeAPITokenEnvelope("https://example.com", "plain-secret-key")
	s.Require().NoError(err)
	s.Contains(rec.Body.String(), envelope)

	gotURL, _, ok := domain.DecodeAPITokenEnvelope(envelope)
	s.True(ok)
	s.Equal("https://example.com", gotURL)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_NonAdmin_Returns403() {
	nonAdmin := &domain.User{
		ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), testAdminID, "hash-123").Return(nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_NonAdmin_Returns403() {
	nonAdmin := &domain.User{
		ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_CreateFails_ShowsError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).
		Return("", domain.ErrFeatureNotSupported)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to create API key.", "create failure renders an inline error")
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokeFails_RedirectsWithError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), testAdminID, "hash-123").Return(domain.ErrNotFound)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "key_error")
}
