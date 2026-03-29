package http

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type rateLimitMiddleware struct {
	limiters map[string]*ipLimiter
	next     http.Handler
	mu       sync.Mutex
	rps      rate.Limit
	burst    int
}

func withRateLimit(rps, burst int, next http.Handler) http.Handler {
	rl := &rateLimitMiddleware{
		limiters: make(map[string]*ipLimiter),
		rps:      rate.Limit(rps),
		burst:    burst,
		next:     next,
	}

	go rl.cleanup()

	return rl
}

func (rl *rateLimitMiddleware) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ip := clientIP(req)
	limiter := rl.getLimiter(ip)

	if !limiter.Allow() {
		http.Error(rw, "too many requests", http.StatusTooManyRequests)
		return
	}

	rl.next.ServeHTTP(rw, req)
}

func (rl *rateLimitMiddleware) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, ok := rl.limiters[ip]
	if !ok {
		entry = &ipLimiter{limiter: rate.NewLimiter(rl.rps, rl.burst)}
		rl.limiters[ip] = entry
	}

	entry.lastSeen = time.Now()

	return entry.limiter
}

func (rl *rateLimitMiddleware) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()

		for ip, entry := range rl.limiters {
			if time.Since(entry.lastSeen) > 3*time.Minute {
				delete(rl.limiters, ip)
			}
		}

		rl.mu.Unlock()
	}
}

func clientIP(req *http.Request) string {
	if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
		if idx := indexOf(forwarded, ','); idx != -1 {
			return forwarded[:idx]
		}

		return forwarded
	}

	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}

	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}

	return host
}

func indexOf(str string, ch byte) int {
	for idx := range len(str) {
		if str[idx] == ch {
			return idx
		}
	}

	return -1
}
