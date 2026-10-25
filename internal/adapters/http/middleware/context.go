// Package middleware provides the HTTP request stack and its shared request context.
package middleware

import (
	"context"
	"net"
	"net/http"
)

type contextKey uint8

const (
	keyRequestID contextKey = 1
	keyNonce     contextKey = 2
	keyUser      contextKey = 3
	keyCSRF      contextKey = 4
	keyLogUser   contextKey = 5
	keyAPIToken  contextKey = 6
)

// logUser is a request-scoped holder the auth layer fills with the resolved identity so the
// outer logging middleware can report it. A pointer is used because WithLogging runs OUTSIDE
// the auth middleware: the user set via r.WithContext downstream is not visible on the logger's
// request, but a shared pointer installed before next() is mutated in place and read after.
type logUser struct {
	name  string
	admin bool
}

// setLogUser records the identity for the request log, if the holder is present.
func setLogUser(ctx context.Context, name string, admin bool) {
	if lu, ok := ctx.Value(keyLogUser).(*logUser); ok {
		lu.name = name
		lu.admin = admin
	}
}

func NonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyNonce).(string)

	return v
}

const SchemeHTTPS = "https"

// SchemeHTTP is the non-TLS URL scheme, paired with SchemeHTTPS when building absolute links.
const SchemeHTTP = "http"

// RequestScheme returns the URL scheme ("http" or "https") to use when building absolute links
// back to this server: the operator-forced override when set (--public-scheme, see
// Handler.WithForcedScheme), otherwise the auto-detected value from IsHTTPS.
func RequestScheme(r *http.Request, forcedScheme string, trustedProxies []*net.IPNet) string {
	if forcedScheme != "" {
		return forcedScheme
	}

	if IsHTTPS(r, "", trustedProxies) {
		return SchemeHTTPS
	}

	return SchemeHTTP
}

// IsHTTPS reports whether the request must be treated as HTTPS — the decision behind the
// cookie Secure attribute and HSTS. An operator-forced "https" (--public-scheme) is honoured
// unconditionally: that flag is the recommended setup behind a TLS-terminating load balancer
// whose address is not stable enough for --trusted-proxies, so the request itself arrives as
// plain HTTP with no trustworthy X-Forwarded-Proto. Any other forcedScheme falls back to
// detection — a forced "http" only affects link building and never strips Secure from a
// cookie that really is travelling over TLS. X-Forwarded-Proto is only trusted when the remote
// address belongs to a trusted proxy; spoofed headers from untrusted clients are ignored.
func IsHTTPS(r *http.Request, forcedScheme string, trustedProxies []*net.IPNet) bool {
	if forcedScheme == SchemeHTTPS || r.TLS != nil {
		return true
	}

	if len(trustedProxies) == 0 {
		return false
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	return isTrustedProxy(host, trustedProxies) && r.Header.Get("X-Forwarded-Proto") == SchemeHTTPS
}
