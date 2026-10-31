package http_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// loginLockThreshold mirrors the production per-username failure threshold on POST /totp-login.
// Kept as a literal so the suite pins the documented contract ("ten failures") rather than
// whatever the constant happens to be.
const loginLockThreshold = 10

const (
	lockTestPassword = "ValidP@ss12!"
	lockTestGoodCode = "123456"
	lockTestBadCode  = "000000"
)

// LoginLockoutSuite exercises the per-username lockout on POST /totp-login through the real
// router: CSRF guard, per-IP TOTP limiter, then the account lock. Every attempt comes from its
// own client IP unless a test says otherwise, so the per-IP limiter (10/min/IP) never engages
// and any 429 observed is the account lock's doing.
type LoginLockoutSuite struct {
	suite.Suite

	ctrl   *gomock.Controller
	auth   *MockAuthManager
	router http.Handler
	nextIP int
}

func TestLoginLockout(t *testing.T) {
	suite.Run(t, new(LoginLockoutSuite))
}

func (s *LoginLockoutSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.auth = NewMockAuthManager(s.ctrl)
	s.nextIP = 0

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
}

// freshIP returns a client address never used before in this test, keeping each attempt in
// its own per-IP rate-limit bucket.
func (s *LoginLockoutSuite) freshIP() string {
	s.nextIP++

	return fmt.Sprintf("198.51.100.%d:4000", s.nextIP)
}

// login POSTs one /totp-login attempt from remoteAddr and returns the recorded response.
func (s *LoginLockoutSuite) login(remoteAddr, username, code string) *httptest.ResponseRecorder {
	form := withCSRF(url.Values{"username": {username}, "password": {lockTestPassword}, "code": {code}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", testCSRFToken))
	req.RemoteAddr = remoteAddr

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	return rec
}

// expectFailures arms the auth mock to reject `times` attempts for username — the usecase
// answers an unknown username with the very same ErrInvalidTOTP, so this also models "nobody".
func (s *LoginLockoutSuite) expectFailures(username string, times int) {
	s.auth.EXPECT().
		Login(gomock.Any(), username, lockTestPassword, lockTestBadCode, gomock.Any(), gomock.Any(), gomock.Any()).
		Return("", domain.ErrInvalidTOTP).
		Times(times)
}

func (s *LoginLockoutSuite) expectSuccess(username string) {
	s.auth.EXPECT().
		Login(gomock.Any(), username, lockTestPassword, lockTestGoodCode, gomock.Any(), gomock.Any(), gomock.Any()).
		Return("sess-"+username, nil)
}

// failFromDistinctIPs sends `times` failing attempts for username, each from a fresh IP.
func (s *LoginLockoutSuite) failFromDistinctIPs(username string, times int) {
	for range times {
		rec := s.login(s.freshIP(), username, lockTestBadCode)
		s.Require().Equal(http.StatusSeeOther, rec.Code, "a failure below the threshold must reach the handler")
		s.Require().Contains(rec.Header().Get("Location"), "/login?error=1")
	}
}

// attemptWhileLocked sends one attempt with the CORRECT credentials for username from
// remoteAddr. The mock would accept it (as the real usecase would), so if the request leaks past
// the lock it signs in — the response then shows a 303 to "/" instead of the expected 429. The
// returned counter records how many such leaks reached the usecase.
func (s *LoginLockoutSuite) attemptWhileLocked(remoteAddr, username string) (*httptest.ResponseRecorder, *int) {
	leaks := new(int)

	s.auth.EXPECT().
		Login(gomock.Any(), username, lockTestPassword, lockTestGoodCode, gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_, _, _, _, _, _ any) (string, error) {
			*leaks++

			return "sess-leaked", nil
		}).
		AnyTimes()

	return s.login(remoteAddr, username, lockTestGoodCode), leaks
}

// requireLocked asserts the attempt was throttled by the account lock and never reached the
// usecase.
func (s *LoginLockoutSuite) requireLocked(rec *httptest.ResponseRecorder, leaks *int) {
	s.Require().Equal(http.StatusTooManyRequests, rec.Code,
		"account must be locked after %d failures; got Location=%q", loginLockThreshold, rec.Header().Get("Location"))
	s.JSONEq(`{"message":"too many failed attempts, try again later"}`, rec.Body.String())
	s.NotEmpty(rec.Header().Get("Retry-After"), "a locked response must say when to retry")
	s.Empty(rec.Result().Cookies(), "a locked response must never issue a session cookie")
	s.Zero(*leaks, "a locked attempt must not reach the auth usecase")
}

// TestDistributedFailures_LockAccountEvenWithCorrectCredentials is M-3 itself: ten failures for
// one username spread over ten client IPs never trip the per-IP limiter, yet the account must
// lock — and the lock must hold even when the next attempt carries the correct credentials,
// otherwise the lock would be a "was that password right?" oracle.
func (s *LoginLockoutSuite) TestDistributedFailures_LockAccountEvenWithCorrectCredentials() {
	s.expectFailures("alice", loginLockThreshold)
	s.failFromDistinctIPs("alice", loginLockThreshold)

	s.requireLocked(s.attemptWhileLocked(s.freshIP(), "alice"))
}

// TestLockedUsername_DoesNotAffectOtherUserOnSameIP: buckets are per username, so bob — even
// from an IP alice's attacker used — still reaches the usecase and signs in.
func (s *LoginLockoutSuite) TestLockedUsername_DoesNotAffectOtherUserOnSameIP() {
	s.expectFailures("alice", loginLockThreshold)
	s.failFromDistinctIPs("alice", loginLockThreshold)

	sharedIP := "198.51.100.1:4000" // the first IP the alice attempts used

	s.requireLocked(s.attemptWhileLocked(sharedIP, "alice"))

	s.expectSuccess("bob")

	rec := s.login(sharedIP, "bob", lockTestGoodCode)
	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/", rec.Header().Get("Location"))
}

// TestSuccessBelowThreshold_ResetsCounter: nine failures, one success, then another nine
// failures plus one more attempt must all reach the usecase — the success wiped the slate.
func (s *LoginLockoutSuite) TestSuccessBelowThreshold_ResetsCounter() {
	s.expectFailures("alice", 2*(loginLockThreshold-1)+1)
	s.failFromDistinctIPs("alice", loginLockThreshold-1)

	s.expectSuccess("alice")

	rec := s.login(s.freshIP(), "alice", lockTestGoodCode)
	s.Require().Equal(http.StatusSeeOther, rec.Code)
	s.Require().Equal("/", rec.Header().Get("Location"))

	s.failFromDistinctIPs("alice", loginLockThreshold)

	s.requireLocked(s.attemptWhileLocked(s.freshIP(), "alice"))
}

// TestUnknownUsername_LockedIdentically: the lock must not be an existence oracle. "nobody"
// (which the usecase rejects with the same ErrInvalidTOTP as a wrong password) locks after the
// same number of failures, with the same status, headers and body as an existing account.
func (s *LoginLockoutSuite) TestUnknownUsername_LockedIdentically() {
	s.expectFailures("alice", loginLockThreshold)
	s.expectFailures("nobody", loginLockThreshold)
	s.failFromDistinctIPs("alice", loginLockThreshold)
	s.failFromDistinctIPs("nobody", loginLockThreshold)

	known, knownLeaks := s.attemptWhileLocked(s.freshIP(), "alice")
	unknown, unknownLeaks := s.attemptWhileLocked(s.freshIP(), "nobody")

	s.requireLocked(known, knownLeaks)
	s.requireLocked(unknown, unknownLeaks)
	s.Equal(known.Code, unknown.Code)
	s.Equal(known.Body.String(), unknown.Body.String())
	s.Equal(known.Header().Get("Content-Type"), unknown.Header().Get("Content-Type"))
}

// TestUsernameKey_IsCaseAndWhitespaceInsensitive: the bucket key is the trimmed, lower-cased
// username, so an attacker cannot rotate "Alice", "alice ", "ALICE" to get fresh buckets.
func (s *LoginLockoutSuite) TestUsernameKey_IsCaseAndWhitespaceInsensitive() {
	half := loginLockThreshold / 2

	s.expectFailures("Alice", half)
	s.expectFailures("alice ", loginLockThreshold-half)
	s.failFromDistinctIPs("Alice", half)
	s.failFromDistinctIPs("alice ", loginLockThreshold-half)

	s.requireLocked(s.attemptWhileLocked(s.freshIP(), "alice"))
	s.requireLocked(s.attemptWhileLocked(s.freshIP(), " ALICE"))
}
