package http

import (
	"net"
	"net/http"
	"strings"
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

func withRateLimit(rps, burst int, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	limiterCache := gcache.New(maxTrackedIPs).
		ARC().
		Expiration(limiterTTL).
		LoaderFunc(func(_ any) (any, error) {
			return rate.NewLimiter(rate.Limit(rps), burst), nil
		}).
		Build()

	return &rateLimitMiddleware{cache: limiterCache, trustedProxies: trustedProxies, next: next}
}

func (rl *rateLimitMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, rl.trustedProxies)

	cached, err := rl.cache.Get(ip)
	if err != nil {
		// LoaderFunc never errors; this path is unreachable in practice.
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	limiter, ok := cached.(*rate.Limiter)
	if !ok {
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	if !limiter.Allow() {
		http.Error(w, "too many requests", http.StatusTooManyRequests)

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

	if len(trustedProxies) > 0 && isTrustedProxy(remoteHost, trustedProxies) {
		if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
			// X-Forwarded-For may be a comma-separated list; the leftmost entry
			// is the original client IP as set by the first proxy in the chain.
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}

		if realIP := strings.TrimSpace(req.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}

	return remoteHost
}

// withTOTPRateLimit returns a HandlerFunc wrapper that caps POST /totp-login at
// totpRatePerMin attempts per minute per client IP. The burst equals the rate cap
// so an attacker can make at most 10 attempts before being throttled.
func withTOTPRateLimit(trustedProxies []*net.IPNet, next http.HandlerFunc) http.HandlerFunc {
	cache := gcache.New(maxTrackedIPs).
		ARC().
		Expiration(limiterTTL).
		LoaderFunc(func(_ any) (any, error) {
			return rate.NewLimiter(rate.Every(time.Minute/totpRatePerMin), totpRatePerMin), nil
		}).
		Build()

	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, trustedProxies)

		cached, err := cache.Get(ip)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}

		limiter, ok := cached.(*rate.Limiter)
		if !ok {
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}

		if !limiter.Allow() {
			http.Error(w, "too many requests", http.StatusTooManyRequests)

			return
		}

		next(w, r)
	}
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
