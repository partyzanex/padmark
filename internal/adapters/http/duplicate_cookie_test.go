package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	time "time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// Session IDs used by DuplicateCookieSuite: the victim's legitimate host-only session and the
// attacker's own session, tossed into the victim's browser from a sibling subdomain.
const (
	dupVictimSession   = "victim-sess"
	dupAttackerSession = "attacker-sess"
)

// DuplicateCookieSuite covers audit finding L-1 end to end at the server boundary ("cookie
// tossing"): a sibling subdomain (evil.example.com) can set padmark_csrf / padmark_session with
// Domain=example.com. The browser then sends two cookies with the same name, ordered by RFC 6265
// §5.4 (longer Path first, then earlier creation time), so an attacker who picks a longer Path
// (e.g. Path=/admin) — or simply plants the cookie first — controls which one comes first.
// net/http's Request.Cookie returns the FIRST same-name cookie, and that is what CSRFGuard and
// ExtractSessionID read. Neither cookie uses the __Host- prefix, which is what would stop a
// sibling subdomain from setting them at all.
type DuplicateCookieSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	auth     *MockAuthManager
	router   http.Handler
	resolved []string
	invites  int
}

func TestDuplicateCookie(t *testing.T) {
	suite.Run(t, new(DuplicateCookieSuite))
}

func (s *DuplicateCookieSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.auth = NewMockAuthManager(s.ctrl)
	s.resolved = nil
	s.invites = 0

	manager := NewMockNoteManager(s.ctrl)
	linkManager := NewMockLinkManager(s.ctrl)

	handler := adhttp.NewHandler(manager, linkManager, discardLog()).WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(manager, linkManager, NewMockPinger(s.ctrl), discardLog())

	router, err := adhttp.NewRouter(handler, ogen, &adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	s.Require().NoError(err)

	s.router = router

	admin := &domain.User{ID: testAdminID, Username: "admin", IsAdmin: true,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	attacker := &domain.User{ID: testUserID2, Username: "mallory", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}

	// Permissive expectations: the tests assert on what was resolved/issued, so a request the
	// desired behaviour would refuse must not abort the test through an unexpected mock call.
	s.auth.EXPECT().GetSession(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, sessID string) (*domain.User, error) {
			s.resolved = append(s.resolved, sessID)

			switch sessID {
			case dupVictimSession:
				return admin, nil
			case dupAttackerSession:
				return attacker, nil
			default:
				return nil, domain.ErrNotFound
			}
		}).AnyTimes()
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).
		DoAndReturn(func(context.Context, any, any) (string, error) {
			s.invites++

			return "tok-planted", nil
		}).AnyTimes()
	s.auth.EXPECT().ListUsers(gomock.Any(), gomock.Any()).Return([]*domain.User{admin}, nil).AnyTimes()
	s.auth.EXPECT().ListAPITokens(gomock.Any(), gomock.Any()).Return([]*auth.APITokenInfo{}, nil).AnyTimes()
}

func (s *DuplicateCookieSuite) TearDownTest() {
	s.ctrl.Finish()
}

// postInvite sends the cross-site auto-submitted form an attacker page would post to
// /admin/invite: csrf_token=formToken, with cookies in exactly the given order.
func (s *DuplicateCookieSuite) postInvite(formToken string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	form := url.Values{"csrf_token": {formToken}}
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	return rec
}

func csrfCookieValue(value string) *http.Cookie {
	return httptestutil.RequestCookie("padmark_csrf", value)
}

func sessionCookieValue(value string) *http.Cookie {
	return httptestutil.RequestCookie("padmark_session", value)
}

// TestDuplicateCSRFCookie_AttackerFirst_Rejected is INTENTIONALLY RED today (audit L-1).
//
// The attacker mints a token by loading any page of the site (tokens are HMAC(nonce) only, not
// bound to a session, so any issued token validates for any victim), tosses it as padmark_csrf
// from a sibling subdomain so it precedes the victim's host-only cookie, and auto-submits a form
// carrying the same token. The victim's SameSite=Strict session cookie is sent (sibling
// subdomains are same-site), CSRFGuard compares the FIRST cookie with the field, both are the
// attacker's — and the admin action runs.
//
// Desired: an ambiguous request carrying more than one padmark_csrf cookie is refused, so the
// invite is never generated. Today it answers 200 and calls GenerateInvite.
func (s *DuplicateCookieSuite) TestDuplicateCSRFCookie_AttackerFirst_Rejected() {
	attackerToken := adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret)
	victimToken := adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret)

	rec := s.postInvite(attackerToken,
		csrfCookieValue(attackerToken), csrfCookieValue(victimToken), sessionCookieValue(dupVictimSession))

	s.Equal(http.StatusForbidden, rec.Code, "duplicate padmark_csrf cookies must be refused")
	s.Zero(s.invites, "a planted CSRF cookie must not authorise an admin action")
}

// TestDuplicateCSRFCookie_VictimFirst_Rejected pins the other order: when the victim's own cookie
// comes first, the attacker's form token does not match it and CSRFGuard refuses — the attack
// depends entirely on winning the cookie order.
func (s *DuplicateCookieSuite) TestDuplicateCSRFCookie_VictimFirst_Rejected() {
	attackerToken := adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret)
	victimToken := adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret)

	rec := s.postInvite(attackerToken,
		csrfCookieValue(victimToken), csrfCookieValue(attackerToken), sessionCookieValue(dupVictimSession))

	s.Equal(http.StatusForbidden, rec.Code)
	s.Zero(s.invites)
}

// TestDuplicateSessionCookie_AttackerFirst_NotResolved is INTENTIONALLY RED today (L-1 family,
// session swap / login CSRF). A tossed padmark_session carrying the attacker's own session,
// ordered first, makes the server resolve the attacker's account for the victim's browser: notes
// the victim then creates are owned by — and readable to — the attacker.
//
// Desired: the attacker-planted (first) session is never the one the server resolves. Today
// ExtractSessionID → r.Cookie returns it and GetSession is called with it.
func (s *DuplicateCookieSuite) TestDuplicateSessionCookie_AttackerFirst_NotResolved() {
	req := httptest.NewRequest(http.MethodGet, "/change-password", nil)
	req.AddCookie(sessionCookieValue(dupAttackerSession))
	req.AddCookie(sessionCookieValue(dupVictimSession))

	s.router.ServeHTTP(httptest.NewRecorder(), req)

	s.NotContains(s.resolved, dupAttackerSession,
		"a session cookie tossed in from a sibling subdomain must not be resolved")
}

// TestDuplicateSessionCookie_VictimFirst_NotResolved pins the other order: with the victim's
// cookie first, only the victim's session is looked up — the attacker's never is.
func (s *DuplicateCookieSuite) TestDuplicateSessionCookie_VictimFirst_NotResolved() {
	req := httptest.NewRequest(http.MethodGet, "/change-password", nil)
	req.AddCookie(sessionCookieValue(dupVictimSession))
	req.AddCookie(sessionCookieValue(dupAttackerSession))

	s.router.ServeHTTP(httptest.NewRecorder(), req)

	s.NotContains(s.resolved, dupAttackerSession)
}
