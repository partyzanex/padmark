package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluele/gcache"

	"github.com/partyzanex/padmark/internal/adapters/http/response"
)

const (
	// maxLoginFailures is the number of unsuccessful POST /totp-login attempts for one username,
	// from any number of client IPs, after which that username is locked for loginLockoutDur.
	maxLoginFailures = 10

	// loginLockoutDur runs from the last counted attempt for the username. Requests rejected
	// with 429 are not counted and do not extend the lock, so a victim whose name is being
	// sprayed regains access at most five minutes after the attacker stops.
	loginLockoutDur = 5 * time.Minute

	// maxTrackedLogins bounds the number of usernames whose counters are tracked at once. ARC
	// evicts the least-recently-used entry when the cache is full, so an attacker cycling random
	// usernames cannot grow memory without bound. Usernames that reached the lock threshold are
	// pinned in a separate lockedSet and survive that churn until loginLockoutDur passes.
	maxTrackedLogins = 10_000
)

// loginLockKey is the SHA-256 of the normalised username (see newLoginLockKey). Hashing keeps
// every key the same small size whatever the client sent and keeps raw usernames, which are
// user input, out of process memory dumps.
type loginLockKey [sha256.Size]byte

// newLoginLockKey normalises username with TrimSpace + ToLower before hashing, so "Alice",
// "alice " and "ALICE" share one bucket and cannot be rotated to obtain fresh ones. Two
// accounts whose names differ only in case therefore share a lock — an acceptable cost, as the
// alternative hands an attacker a free bucket per capitalisation.
func newLoginLockKey(username string) loginLockKey {
	return sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
}

// loginAttempts is the per-username state: attempts counted since the last success and the
// instant the entry (and with it any lock) expires.
type loginAttempts struct {
	expires time.Time
	count   int
}

// LoginLockout tracks POST /totp-login attempts per username, complementing the per-IP
// WithTOTPRateLimit: the IP limiter caps one source, this caps one target, so a distributed
// attacker (many IPs, or every client collapsed behind an untrusted proxy) still gets at most
// maxLoginFailures guesses per account per loginLockoutDur.
//
// A fixed window (rather than exponential backoff) mirrors WithFailLockout: it is easy to
// reason about, and it bounds how long an attacker can keep a known username locked to five
// minutes past their last counted attempt — while the per-IP limiter caps how fast they can
// feed it.
type LoginLockout struct {
	cache gcache.Cache
	// locked pins usernames that reached maxLoginFailures so that evicting their counter from
	// the ARC cache (by spraying other usernames) cannot unlock them.
	locked *lockedSet[loginLockKey]
	clock  gcache.Clock
	// mu makes check-and-count atomic so a parallel burst cannot slip more than
	// maxLoginFailures attempts past the gate. It guards only the in-memory cache.
	mu sync.Mutex
}

// NewLoginLockout returns an empty, bounded login lockout state that measures time with clock.
func NewLoginLockout(clock gcache.Clock) *LoginLockout {
	return &LoginLockout{
		cache:  gcache.New(maxTrackedLogins).ARC().Clock(clock).Build(),
		locked: newLockedSet[loginLockKey](),
		clock:  clock,
		mu:     sync.Mutex{},
	}
}

// reserve counts one attempt for key before it is attempted, unless key is locked. Counting up
// front (and refunding via reset on success) means concurrent in-flight attempts are counted
// too. It returns the remaining lock time and false when key is locked.
func (l *LoginLockout) reserve(key loginLockKey) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()

	if remaining, locked := l.locked.remaining(key, now); locked {
		return remaining, false
	}

	state := loginAttempts{expires: time.Time{}, count: 0}

	cached, err := l.cache.Get(key)
	if err == nil {
		if prev, ok := cached.(loginAttempts); ok {
			state = prev
		}
	}

	if state.count >= maxLoginFailures {
		return state.expires.Sub(now), false
	}

	state.count++
	state.expires = now.Add(loginLockoutDur)

	if state.count >= maxLoginFailures {
		l.locked.lock(key, now, state.expires)
	}

	setErr := l.cache.SetWithExpire(key, state, loginLockoutDur)
	if setErr != nil {
		// ARC SetWithExpire never returns an error in practice; on failure the attempt simply
		// goes uncounted rather than locking anyone out.
		return 0, true
	}

	return 0, true
}

// reset clears key after a successful login.
func (l *LoginLockout) reset(key loginLockKey) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cache.Remove(key)
	l.locked.unlock(key)
}

// WithLoginLockout wraps POST /totp-login with the per-username lock. It must run after the
// CSRF guard, which has already parsed the (size-capped) form, so reading "username" here does
// not consume anything the handler needs.
//
// Every attempt is counted on entry; a response that issues a session cookie is a success and
// resets the username's counter. A locked username gets 429 with a fixed JSON body and
// Retry-After, before the auth usecase runs — so the response is identical whether the account
// exists or not and whether the password was right, and cannot serve as an oracle for either.
func WithLoginLockout(lock *LoginLockout, log *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := newLoginLockKey(r.FormValue("username"))

		retryAfter, allowed := lock.reserve(key)
		if !allowed {
			log.WarnContext(r.Context(), "login attempt rejected: username locked after repeated failures",
				"username_sha256", hex.EncodeToString(key[:8]))
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
			response.WriteJSONErrorStatus(w, r, log, http.StatusTooManyRequests, "too many failed attempts, try again later")

			return
		}

		next(w, r)

		if sessionIssued(w.Header()) {
			lock.reset(key)
		}
	}
}

// retryAfterSeconds rounds remaining up to whole seconds, never below one.
func retryAfterSeconds(remaining time.Duration) int {
	return max(1, int(math.Ceil(remaining.Seconds())))
}

// sessionIssued reports whether the handler set a live session cookie — the one observable
// marker of a successful login, since /totp-login answers both outcomes with a 303.
func sessionIssued(header http.Header) bool {
	for _, raw := range header.Values("Set-Cookie") {
		cookie, err := http.ParseSetCookie(raw)
		if err == nil && cookie.Name == SessionCookieName && cookie.Value != "" && cookie.MaxAge >= 0 {
			return true
		}
	}

	return false
}
