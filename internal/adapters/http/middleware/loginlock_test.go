package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluele/gcache"
	"github.com/partyzanex/padmark/tests/httptestutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loginLockFixture wraps a stub login handler with WithLoginLockout on a fake clock. The stub
// "signs in" (sets the session cookie) when the form's code is "ok" and fails otherwise,
// mirroring TOTPLoginHandler's 303-for-both-outcomes behaviour.
type loginLockFixture struct {
	clock    gcache.FakeClock
	handler  http.HandlerFunc
	upstream atomic.Int32
}

func newLoginLockFixture() *loginLockFixture {
	fix := &loginLockFixture{clock: gcache.NewFakeClock(), handler: nil, upstream: atomic.Int32{}}
	stub := func(w http.ResponseWriter, r *http.Request) {
		fix.upstream.Add(1)

		if r.FormValue("code") == "ok" {
			http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "sess", MaxAge: 60,
				Quoted: false, Path: "", Domain: "", Expires: time.Time{}, RawExpires: "",
				Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
				Partitioned: false, Raw: "",
				Unparsed: nil,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)

			return
		}

		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
	}
	fix.handler = WithLoginLockout(NewLoginLockout(fix.clock), testDiscardLog, stub)

	return fix
}

func (f *loginLockFixture) attempt(username, code string) *httptest.ResponseRecorder {
	form := url.Values{"username": {username}, "code": {code}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	f.handler(rec, req)

	return rec
}

func (f *loginLockFixture) fail(t *testing.T, username string, times int) {
	t.Helper()

	for range times {
		require.Equal(t, http.StatusSeeOther, f.attempt(username, "bad").Code)
	}
}

// TestWithLoginLockout_LockExpires_ThenSuccessResets covers the time dimension the HTTP-level
// suite cannot: the lock lifts after loginLockoutDur, Retry-After reports the remaining time,
// 429s do not extend the lock, and a success after expiry starts from a clean counter.
func TestWithLoginLockout_LockExpires_ThenSuccessResets(t *testing.T) {
	fix := newLoginLockFixture()
	fix.fail(t, "alice", maxLoginFailures)

	fix.clock.Advance(time.Minute)

	locked := fix.attempt("alice", "ok")
	assert.Equal(t, http.StatusTooManyRequests, locked.Code)
	assert.Equal(t, "240", locked.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"message":"too many failed attempts, try again later"}`, locked.Body.String())

	// A 429 at minute four must not push the expiry out.
	fix.clock.Advance(3 * time.Minute)
	assert.Equal(t, http.StatusTooManyRequests, fix.attempt("alice", "ok").Code)

	fix.clock.Advance(time.Minute + time.Second)

	ok := fix.attempt("alice", "ok")
	assert.Equal(t, http.StatusSeeOther, ok.Code)
	assert.Equal(t, "/", ok.Header().Get("Location"))

	fix.fail(t, "alice", maxLoginFailures)
	assert.Equal(t, http.StatusTooManyRequests, fix.attempt("alice", "ok").Code)
	// Two rounds of failures plus the one success reached upstream; no 429 did.
	assert.Equal(t, int32(2*maxLoginFailures+1), fix.upstream.Load())
}

// TestWithLoginLockout_ParallelBurst_AdmitsOnlyThreshold: attempts are counted on entry, so a
// burst of concurrent guesses cannot all pass the gate before the first failure is recorded.
func TestWithLoginLockout_ParallelBurst_AdmitsOnlyThreshold(t *testing.T) {
	fix := newLoginLockFixture()

	const burst = 5 * maxLoginFailures

	var (
		wg      sync.WaitGroup
		limited atomic.Int32
	)

	for range burst {
		wg.Go(func() {
			if fix.attempt("alice", "bad").Code == http.StatusTooManyRequests {
				limited.Add(1)
			}
		})
	}

	wg.Wait()

	assert.Equal(t, int32(maxLoginFailures), fix.upstream.Load())
	assert.Equal(t, int32(burst-maxLoginFailures), limited.Load())
}

func TestNewLoginLockKey_NormalisesCaseAndSpace(t *testing.T) {
	assert.Equal(t, newLoginLockKey("alice"), newLoginLockKey("  ALICE\t"))
	assert.NotEqual(t, newLoginLockKey("alice"), newLoginLockKey("bob"))
}

func TestSessionIssued(t *testing.T) {
	cases := map[string]struct {
		cookie *http.Cookie
		want   bool
	}{
		//nolint:gosec // G124: test cookie, attributes irrelevant to sessionIssued
		"live session": {cookie: &http.Cookie{Name: SessionCookieName, Value: "s", MaxAge: 60,
			Quoted: false, Path: "", Domain: "", Expires: time.Time{}, RawExpires: "",
			Secure: false, HttpOnly: false, SameSite: 0, Partitioned: false, Raw: "",
			Unparsed: nil,
		}, want: true},
		//nolint:gosec // G124: test cookie, attributes irrelevant to sessionIssued
		"session delete": {cookie: &http.Cookie{Name: SessionCookieName, Value: "", MaxAge: -1,
			Quoted: false, Path: "", Domain: "", Expires: time.Time{}, RawExpires: "",
			Secure: false, HttpOnly: false, SameSite: 0, Partitioned: false, Raw: "",
			Unparsed: nil,
		}, want: false},

		"other cookie": {cookie: httptestutil.RequestCookie("padmark_csrf", "tok"), want: false},
		"no cookie":    {cookie: nil, want: false},
	}

	for name, tcase := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if tcase.cookie != nil {
				http.SetCookie(rec, tcase.cookie)
			}

			assert.Equal(t, tcase.want, sessionIssued(rec.Header()))
		})
	}
}
