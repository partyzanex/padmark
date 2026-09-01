package http

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bluele/gcache"
	"golang.org/x/time/rate"
)

// maxTrackedIPs is the capacity of the ARC cache that maps client IPs to rate
// limiters. ARC evicts entries automatically when the cache is full, bounding
// memory usage regardless of how many unique IPs are seen.
const maxTrackedIPs = 100_000

// limiterTTL is the idle lifetime of a per-IP entry. An IP that has not been
// seen for this duration is evicted and will receive a fresh limiter bucket on
// the next request.
const limiterTTL = 10 * time.Minute

// totpRatePerMin is the per-IP request cap on POST /totp-login.
// 10 attempts/min means a 6-digit brute force takes ~16 hours per IP.
const totpRatePerMin = 10

type rateLimitMiddleware struct {
	cache          gcache.Cache
	next           http.Handler
	trustedProxies []*net.IPNet
}

// newIPRateLimiterCache builds an ARC cache mapping client IP to a *rate.Limiter, evicting
// idle entries after limiterTTL. newLimiter is called once per IP, the first time it's seen —
// used by both withRateLimit (general per-request limit) and withTOTPRateLimit (POST
// /totp-login-specific limit), which need independent per-IP buckets since they track
// unrelated concerns and must not share state.
//
//nolint:ireturn // gcache.Cache is an interface by design; no concrete type is exposed by the library
func newIPRateLimiterCache(newLimiter func() *rate.Limiter) gcache.Cache {
	return gcache.New(maxTrackedIPs).
		ARC().
		Expiration(limiterTTL).
		LoaderFunc(func(_ any) (any, error) {
			return newLimiter(), nil
		}).
		Build()
}

// allowRequest reports whether ip is currently under its rate limit in cache. On false it has
// already written the appropriate error response (500 on cache lookup failure — unreachable in
// practice since LoaderFunc never errors; 429 when the limit is exceeded), so the caller must
// stop handling the request.
func allowRequest(w http.ResponseWriter, cache gcache.Cache, ip string) bool {
	cached, err := cache.Get(ip)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return false
	}

	limiter, ok := cached.(*rate.Limiter)
	if !ok {
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return false
	}

	if !limiter.Allow() {
		http.Error(w, "too many requests", http.StatusTooManyRequests)

		return false
	}

	return true
}

func withRateLimit(rps, burst int, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	limiterCache := newIPRateLimiterCache(func() *rate.Limiter {
		return rate.NewLimiter(rate.Limit(rps), burst)
	})

	return &rateLimitMiddleware{cache: limiterCache, trustedProxies: trustedProxies, next: next}
}

func (rl *rateLimitMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, rl.trustedProxies)

	if !allowRequest(w, rl.cache, ip) {
		return
	}

	rl.next.ServeHTTP(w, r)
}

// clientIP returns the real client IP for rate limiting purposes.
// X-Forwarded-For and X-Real-IP are only trusted when the direct connection
// originates from a trusted proxy CIDR. If trustedProxies is empty, these
// headers are ignored and RemoteAddr is used directly.
func clientIP(req *http.Request, trustedProxies []*net.IPNet) string {
	remoteHost, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		remoteHost = req.RemoteAddr
	}

	if len(trustedProxies) == 0 || !isTrustedProxy(remoteHost, trustedProxies) {
		return remoteHost
	}

	if ip := clientIPFromForwarded(req.Header.Get("X-Forwarded-For"), trustedProxies); ip != "" {
		return ip
	}

	if realIP := net.ParseIP(strings.TrimSpace(req.Header.Get("X-Real-IP"))); realIP != nil {
		return realIP.String()
	}

	return remoteHost
}

// clientIPFromForwarded extracts the client IP from an X-Forwarded-For list by walking it
// right to left: proxies append entries, they do not rewrite the list, so a client can plant
// arbitrary values at the left end — trusting the leftmost entry would let it rotate a fresh
// rate-limit bucket per request. Each entry the trusted chain itself appended is a trusted
// proxy IP and is skipped; the first valid non-trusted entry to its left is the client.
// A malformed entry breaks the chain — entries left of it were set by an unknown party —
// so a parse failure aborts the walk instead of skipping ahead, and the caller falls back to
// RemoteAddr. An empty result means no usable client IP was found.
func clientIPFromForwarded(forwarded string, trustedProxies []*net.IPNet) string {
	if forwarded == "" {
		return ""
	}

	parts := strings.Split(forwarded, ",")
	for _, part := range slices.Backward(parts) {
		candidate := strings.TrimSpace(part)
		if net.ParseIP(candidate) == nil {
			return ""
		}

		if isTrustedProxy(candidate, trustedProxies) {
			continue
		}

		return candidate
	}

	return ""
}

// withTOTPRateLimit returns a HandlerFunc wrapper that caps POST /totp-login at
// totpRatePerMin attempts per minute per client IP. The burst equals the rate cap
// so an attacker can make at most 10 attempts before being throttled.
func withTOTPRateLimit(trustedProxies []*net.IPNet, next http.HandlerFunc) http.HandlerFunc {
	cache := newIPRateLimiterCache(func() *rate.Limiter {
		return rate.NewLimiter(rate.Every(time.Minute/totpRatePerMin), totpRatePerMin)
	})

	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, trustedProxies)

		if !allowRequest(w, cache, ip) {
			return
		}

		next(w, r)
	}
}

// warnUnconfiguredProxy logs once, on the first request that arrives with a forwarding header
// while no proxy is trusted. That combination is evidence that a proxy really does sit in front
// of the server but --trusted-proxies was left empty: the headers are then ignored, every
// request is attributed to the proxy's own address, and all clients collapse into a single
// rate-limit bucket — one noisy client throttles everyone else, including on TOTP login.
//
// The signal is the header, not the listen address: a server exposed directly with no proxy is
// a perfectly good configuration and must stay silent. A client can of course send the header
// to a directly-exposed server and trigger the warning, which is why this is one log line and
// not an error. Install only when len(trustedProxies) == 0; wrap nothing otherwise.
func warnUnconfiguredProxy(log *slog.Logger, next http.Handler) http.Handler {
	var once sync.Once

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
			ctx := r.Context()

			once.Do(func() {
				log.WarnContext(ctx,
					"request carries a forwarding header but no proxy is trusted: "+
						"the header is ignored and every client shares one rate-limit bucket; "+
						"set --trusted-proxies to the proxy's address if it is in front of this server",
					"remote_addr", r.RemoteAddr,
				)
			})
		}

		next.ServeHTTP(w, r)
	})
}

func isTrustedProxy(ip string, proxies []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	for _, cidr := range proxies {
		if cidr.Contains(parsed) {
			return true
		}
	}

	return false
}
