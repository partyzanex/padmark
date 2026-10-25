package http_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
)

// PathNormalizationSuite drives the real router (auth configured, mocked managers) with request
// targets a raw client can send but a browser would normally canonicalise: doubled slashes, dot
// segments, percent-encoded letters and percent-encoded slashes. The auth middleware classifies
// routes on the decoded r.URL.Path (isPublicPath / isPublicRoute / isNoteIDPath), while
// http.ServeMux cleans the escaped path and matches it segment by segment, unescaping each
// segment on its own. These tests pin that the two views never disagree in a way that lets an
// anonymous caller reach a protected handler.
//
// The auth manager mock carries no expectations: any ListUsers / GenerateInvite /
// CreateAPIToken / ChangePassword call — i.e. a protected handler actually running — fails the
// test through gomock.
type PathNormalizationSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	revealStore *MockRevealTokenStore
	auth        *MockAuthManager
	router      http.Handler
}

func TestPathNormalization(t *testing.T) {
	suite.Run(t, new(PathNormalizationSuite))
}

func (s *PathNormalizationSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.revealStore = NewMockRevealTokenStore(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)

	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.auth).
		WithRevealStore(s.revealStore)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, NewMockPinger(s.ctrl), discardLog)

	router, err := adhttp.NewRouter(handler, ogen, &adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
	})
	s.Require().NoError(err)

	s.router = router
}

func (s *PathNormalizationSuite) TearDownTest() {
	s.ctrl.Finish()
}

// recordPeeks makes every note lookup miss and returns the IDs the note handler looked up, so a
// case can assert whether (and as which slug) a request was dispatched to the note catch-all.
func (s *PathNormalizationSuite) recordPeeks() *[]string {
	peeked := []string{}

	s.manager.EXPECT().Peek(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*domain.Note, error) {
			peeked = append(peeked, id)

			return nil, domain.ErrNotFound
		}).AnyTimes()

	return &peeked
}

// rawRequest parses method + target exactly as net/http's server does for a raw request line,
// so r.URL.Path / r.URL.RawPath hold what an arbitrary client could send. A POST carries a valid
// CSRF cookie and matching form field, so the CSRF guard is not what stops it.
func (s *PathNormalizationSuite) rawRequest(method, target string) *http.Request {
	var head, body strings.Builder

	head.WriteString(method + " " + target + " HTTP/1.1\r\nHost: example.com\r\n")

	if method == http.MethodPost {
		body.WriteString(url.Values{"csrf_token": {testCSRFToken}}.Encode())
		head.WriteString("Content-Type: application/x-www-form-urlencoded\r\n")
		head.WriteString("Content-Length: " + strconv.Itoa(body.Len()) + "\r\n")
		head.WriteString("Cookie: padmark_csrf=" + testCSRFToken + "\r\n")
	}

	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(head.String() + "\r\n" + body.String())))
	s.Require().NoError(err)

	req.RemoteAddr = "192.0.2.1:1234"

	return req
}

func (s *PathNormalizationSuite) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	return rec
}

// nonCanonicalCase is one anonymous request target and the outcome pinned for it.
type nonCanonicalCase struct {
	method   string
	target   string
	location string // expected Location for a ServeMux canonicalisation redirect
	noteID   string // slug the note catch-all looked up, "" when it was not reached
	status   int
}

// redirected: ServeMux answers 307 to the cleaned path, no handler runs.
func redirected(method, target, location string) nonCanonicalCase {
	return nonCanonicalCase{
		method: method, target: target, location: location, noteID: "", status: http.StatusTemporaryRedirect,
	}
}

// refused: the auth middleware (or ServeMux) answers status before any handler runs.
func refused(method, target string, status int) nonCanonicalCase {
	return nonCanonicalCase{method: method, target: target, location: "", noteID: "", status: status}
}

// asNote: the request is dispatched to the note catch-all and looked up as slug noteID (404).
func asNote(method, target, noteID string) nonCanonicalCase {
	return nonCanonicalCase{method: method, target: target, location: "", noteID: noteID, status: http.StatusNotFound}
}

func nonCanonicalCases() []nonCanonicalCase {
	const get, post = http.MethodGet, http.MethodPost

	return []nonCanonicalCase{
		redirected(get, "//admin", "/admin"),
		redirected(get, "/./admin", "/admin"),
		redirected(get, "/x/../admin", "/admin"),
		redirected(get, "/static/../admin", "/admin"),
		redirected(get, "/r/../admin", "/admin"),
		redirected(get, "//change-password", "/change-password"),
		redirected(post, "//api/tokens", "/api/tokens"),
		redirected(post, "//admin/invite", "/admin/invite"),
		redirected(post, "//admin/api-keys", "/admin/api-keys"),
		refused(get, "/%61dmin", http.StatusUnauthorized),
		refused(get, "/admin/", http.StatusUnauthorized),
		refused(get, "/admin%2F", http.StatusUnauthorized),
		refused(get, "/%63hange-password", http.StatusUnauthorized),
		refused(post, "/%63hange-password", http.StatusUnauthorized),
		refused(post, "/api%2Ftokens", http.StatusUnauthorized),
		refused(post, "/%61pi/tokens", http.StatusUnauthorized),
		refused(post, "/%61pi/invites", http.StatusUnauthorized),
		refused(post, "/%61dmin/invite", http.StatusUnauthorized),
		refused(post, "/admin/%61pi-keys", http.StatusUnauthorized),
		refused(post, "/%61dmin/api-keys", http.StatusUnauthorized),
		refused(post, "/admin%2Fapi-keys", http.StatusUnauthorized),
		refused(get, "/static/%2E%2E/admin", http.StatusNotFound),
		// CONNECT is exempt from ServeMux path cleaning, but no route serves CONNECT.
		refused(http.MethodConnect, "//admin", http.StatusMethodNotAllowed),
		refused(http.MethodConnect, "/admin", http.StatusUnauthorized),
		asNote(get, "/%2Fadmin", "/admin"),
		asNote(get, "/ADMIN", "ADMIN"),
		asNote(get, "/admin;x", "admin;x"),
		asNote(get, "/r/%2E%2E/admin", "r/../admin"),
		asNote(get, "/edit/%2E%2E/admin", "../admin"),
		asNote(post, "/%2Fapi/invites", "/api/invites"),
		asNote(post, "/%2Fadmin/api-keys", "/admin/api-keys"),
		asNote(post, "/static/%2E%2E/admin/invite", "static/../admin/invite"),
	}
}

// TestAnonymousNonCanonicalTargets_NeverReachProtectedHandlers documents what ServeMux (Go 1.22+
// routing, Go 1.27 status codes) does with each shape and pins that none of them reaches a
// protected handler anonymously:
//
//   - Uncleaned paths (//x, /./x, /x/../y, /static/../x): the auth middleware may classify them
//     as public (first segment "" or a public prefix), but ServeMux never dispatches them — it
//     answers 307 to the cleaned path (method and body preserved), which is then re-classified
//     from scratch and gets 401.
//   - Percent-encoded letters (/%61dmin): r.URL.Path is decoded and ServeMux unescapes each
//     segment, so both sides see "admin" — consistent, 401.
//   - Percent-encoded slash in the first segment (/%2Fadmin): the middleware sees Path "//admin"
//     (first segment "", so a public note path), but ServeMux sees ONE segment and dispatches it
//     to the note catch-all with slug "/admin" — a note lookup, never an admin handler.
//   - Encoded slash after a reserved name (/admin%2F, /api%2Ftokens): the middleware sees the
//     reserved first segment and demands auth (fail-closed).
//   - Encoded dot segments (/r/%2E%2E/admin): not cleaned by ServeMux (it cleans the escaped
//     path), so they never match a literal admin pattern; they fall to the note catch-all or 404.
func (s *PathNormalizationSuite) TestAnonymousNonCanonicalTargets_NeverReachProtectedHandlers() {
	peeked := s.recordPeeks()

	for _, tcase := range nonCanonicalCases() {
		s.Run(tcase.method+" "+tcase.target, func() {
			*peeked = (*peeked)[:0]

			rec := s.serve(s.rawRequest(tcase.method, tcase.target))

			s.Equal(tcase.status, rec.Code, "body: %s", rec.Body.String())
			s.Equal(tcase.location, rec.Header().Get("Location"))

			if tcase.noteID == "" {
				s.Empty(*peeked, "must not be dispatched to the note catch-all")
			} else {
				s.Equal([]string{tcase.noteID}, *peeked, "dispatched to the note catch-all as this slug")
			}

			if tcase.location != "" {
				// Follow the canonicalisation redirect the way a client would (307 keeps the
				// method): the clean path is classified again and still needs a credential.
				followed := s.serve(s.rawRequest(tcase.method, tcase.location))
				s.Equal(http.StatusUnauthorized, followed.Code, "the clean path must still demand auth")
			}
		})
	}
}

// TestAnonymousNonCanonicalAdmin_BrowserGetsLoginRedirect pins the browser flavour of the same
// property: a navigation to an encoded admin path ends at /login, never at admin content.
func (s *PathNormalizationSuite) TestAnonymousNonCanonicalAdmin_BrowserGetsLoginRedirect() {
	for _, target := range []string{"/%61dmin", "/admin%2F", "/%61dmin/api-keys"} {
		s.Run(target, func() {
			req := s.rawRequest(http.MethodGet, target)
			req.Header.Set("Sec-Fetch-Dest", "document")

			rec := s.serve(req)

			s.Equal(http.StatusSeeOther, rec.Code)
			s.True(strings.HasPrefix(rec.Header().Get("Location"), "/login?next="), rec.Header().Get("Location"))
		})
	}
}

// TestPercentEncodedLoginPath_IsTheLoginPage pins consistency for a public route: /%6Cogin is
// classified as /login by the middleware AND routed to the login page by ServeMux.
func (s *PathNormalizationSuite) TestPercentEncodedLoginPath_IsTheLoginPage() {
	rec := s.serve(s.rawRequest(http.MethodGet, "/%6Cogin"))

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "<form")
}

// TestPercentEncodedPrivateSlug_StillGated covers the inverse direction: a private note fetched
// through a percent-encoded spelling of its slug reaches the note handler under its real slug,
// and the per-note privacy gate (handlePrivateAuth) still refuses the anonymous caller — no
// View/GetRendered call, no content.
func (s *PathNormalizationSuite) TestPercentEncodedPrivateSlug_StillGated() {
	privacy := domain.PrivacyAuthenticated
	private := &domain.Note{ID: "secret", Content: "top secret", Privacy: &privacy}

	for _, target := range []string{"/%73ecret", "/notes/%73ecret", "/edit/%73ecret", "/%73ecret%2Fsub"} {
		s.Run(target, func() {
			wantID := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(target, "/notes"), "/edit"), "/")
			wantID, err := url.PathUnescape(wantID)
			s.Require().NoError(err)

			s.manager.EXPECT().Peek(gomock.Any(), wantID).Return(private, nil).Times(1)

			rec := s.serve(s.rawRequest(http.MethodGet, target))

			s.Equal(http.StatusUnauthorized, rec.Code)
			s.NotContains(rec.Body.String(), "top secret")
		})
	}
}
