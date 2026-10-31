package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bluele/gcache"
	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/http/response"
)

const (
	// maxFailedAttempts is the number of consecutive wrong edit-code attempts after
	// which this bucket (see failLockoutKey) is locked for failLockoutDur.
	maxFailedAttempts = 10

	// failLockoutDur runs from the last 403 for this bucket.
	// Requests rejected with 429 do not extend the lockout.
	failLockoutDur = 5 * time.Minute

	// maxTrackedFailures bounds the number of failure counters tracked at once.
	// ARC evicts the least-recently-used entry when the cache is full, so counters below the
	// threshold are best-effort; buckets that reached it are pinned in lockedSet instead.
	maxTrackedFailures = 10_000
)

// failLockoutKey identifies one fail-lockout bucket.
//
// Authenticated callers are keyed by user ID alone (IP empty), shared by all their sessions,
// API tokens and client IPs. Anonymous callers (uuid.Nil) are keyed by client IP as well, so an
// attacker exhausting the attempts for a resource locks only its own address, not the
// resource's legitimate anonymous editor elsewhere (finding L-6). Never key on unverified
// credential strings: rotating bogus tokens must not reset a bucket.
type failLockoutKey struct {
	ID       string
	IP       string
	CallerID uuid.UUID
}

const (
	// ipv6LockoutPrefixBits groups anonymous IPv6 callers by /64: a single host or subscriber is
	// normally handed a whole /64, so keying by the full address would let it mint a fresh
	// bucket per request.
	ipv6LockoutPrefixBits = 64
	// ipv6AddressBits is the full IPv6 address width, the second argument to net.CIDRMask.
	ipv6AddressBits = net.IPv6len * 8
)

// newFailLockoutKey derives the bucket for r. The anonymous IP comes from ClientIP, so
// X-Forwarded-For is honoured only from trustedProxies: behind a proxy that is not listed, every
// anonymous caller appears as the proxy's address and they share one bucket per resource again
// (the pre-L-6 behaviour) — configure --trusted-proxies to separate them.
func newFailLockoutKey(r *http.Request, trustedProxies []*net.IPNet) failLockoutKey {
	key := failLockoutKey{ID: r.PathValue("id"), IP: "", CallerID: CallerIDFromCtx(r.Context())}
	if key.CallerID != uuid.Nil {
		return key
	}

	key.IP = ClientIP(r, trustedProxies)

	parsed := net.ParseIP(key.IP)
	if parsed != nil && parsed.To4() == nil {
		key.IP = parsed.Mask(net.CIDRMask(ipv6LockoutPrefixBits, ipv6AddressBits)).String()
	}

	return key
}

// FailLockoutCache holds the per-bucket failure counters (the embedded ARC cache) and the
// buckets that reached maxFailedAttempts. A locked bucket is pinned in locked until failLockoutDur
// passes, because ARC would otherwise evict it once an attacker touches maxTrackedFailures other
// buckets twice each, restarting its counter at zero. Memory is bounded: counters by
// maxTrackedFailures, locks by maxLockedBuckets (and in practice by the failures per duration).
type FailLockoutCache struct {
	gcache.Cache

	locked *lockedSet[failLockoutKey]
}

// NewFailLockoutCache returns an empty, bounded lockout state for WithFailLockout.
func NewFailLockoutCache() *FailLockoutCache {
	return &FailLockoutCache{
		Cache:  gcache.New(maxTrackedFailures).ARC().Build(),
		locked: newLockedSet[failLockoutKey](),
	}
}

// WithFailLockout wraps note/link PUT and DELETE routes after authentication.
// Failures and successful resets affect only the current bucket (see failLockoutKey): another
// user or an anonymous attack cannot block the authenticated owner, and an anonymous attack
// from one IP cannot block an anonymous editor at another. trustedProxies decides which
// forwarded client IP is believed, exactly as for the per-IP rate limiter (ClientIP).
//
// The lockout only throttles online guessing per bucket; an attacker with many addresses gets
// one bucket per address (per /64 for IPv6), which is why caller-chosen edit codes must meet
// domain.MinEditCodeLength. The state is in-process, so replicas do not share it.
func WithFailLockout(
	cache *FailLockoutCache, trustedProxies []*net.IPNet, log *slog.Logger, next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := newFailLockoutKey(r, trustedProxies)

		if isFailLocked(cache, key) {
			response.WriteJSONErrorStatus(w, r, log, http.StatusTooManyRequests, "too many failed attempts, try again later")

			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK, written: false}
		next.ServeHTTP(rec, r)

		switch {
		case rec.status == http.StatusForbidden:
			count := 0

			prev, getErr := cache.Get(key)
			if getErr == nil {
				if prevCount, ok := prev.(int); ok {
					count = prevCount
				}
			}

			count++

			if count >= maxFailedAttempts {
				// Pin the lock outside the evictable counter cache.
				cache.locked.lock(key, time.Now(), time.Now().Add(failLockoutDur))
			}

			setErr := cache.SetWithExpire(key, count, failLockoutDur)
			if setErr != nil {
				// ARC SetWithExpire never returns an error in practice; skip silently.
				return
			}
		case rec.status < http.StatusBadRequest:
			// Success resets only this caller's failures, preserving other buckets.
			cache.Remove(key)
			cache.locked.unlock(key)
		default:
			// Other 4xx and 5xx responses leave the failure count unchanged.
		}
	})
}

// isFailLocked reports whether key is locked, either pinned in the lock set or by a counter
// that already reached the threshold.
func isFailLocked(cache *FailLockoutCache, key failLockoutKey) bool {
	if _, locked := cache.locked.remaining(key, time.Now()); locked {
		return true
	}

	cached, err := cache.Get(key)
	if err != nil {
		return false
	}

	count, ok := cached.(int)

	return ok && count >= maxFailedAttempts
}
