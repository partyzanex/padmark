package http

import (
	"net"
	"net/http"
	"strings"
)

// normalizeHost lower-cases a Host header value or configured entry and strips the port, the
// brackets of an IPv6 literal and a trailing dot, so "Example.COM:8443", "example.com." and
// "example.com" compare equal.
func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)

	hostOnly, _, err := net.SplitHostPort(host)
	if err == nil {
		host = hostOnly
	}

	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	host = strings.TrimSuffix(host, ".")

	return strings.ToLower(host)
}

// withAllowedHosts rejects requests whose Host header is not in hosts with 421 Misdirected
// Request before any handler runs, so a forged Host cannot reach code that echoes it (absolute
// URLs in redirects, invite links, cache keys behind a shared cache). An empty list disables
// the check, which is the default. /healthz and /readyz bypass it: orchestrator probes
// address the pod by IP and would otherwise fail the moment the allow-list is set.
func withAllowedHosts(hosts []string, next http.Handler) http.Handler {
	if len(hosts) == 0 {
		return next
	}

	allowed := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		allowed[normalizeHost(host)] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)

			return
		}

		if _, ok := allowed[normalizeHost(r.Host)]; !ok {
			http.Error(w, "misdirected request: unknown host", http.StatusMisdirectedRequest)

			return
		}

		next.ServeHTTP(w, r)
	})
}
