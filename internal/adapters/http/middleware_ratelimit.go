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

type rateLimitMiddleware struct {
	cache          gcache.Cache
	next           http.Handler
	trustedProxies []*net.IPNet
}

func withRateLimit(rps, burst int, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	c := gcache.New(maxTrackedIPs).
		ARC().
		Expiration(limiterTTL).
		LoaderFunc(func(_ interface{}) (interface{}, error) {
			return rate.NewLimiter(rate.Limit(rps), burst), nil
		}).
		Build()

	return &rateLimitMiddleware{cache: c, trustedProxies: trustedProxies, next: next}
}

func (rl *rateLimitMiddleware) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ip := clientIP(req, rl.trustedProxies)

	v, _ := rl.cache.Get(ip) // LoaderFunc never returns an error
	limiter := v.(*rate.Limiter)

	if !limiter.Allow() {
		http.Error(rw, "too many requests", http.StatusTooManyRequests)
		return
	}

	rl.next.ServeHTTP(rw, req)
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
