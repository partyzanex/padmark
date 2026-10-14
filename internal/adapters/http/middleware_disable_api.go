package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// disabledAPIMessage is the fixed body of every REST/JSON API response while --disable-api is
// set. It carries no operational detail on purpose — the API is off, not erroring.
const (
	disabledAPIMessage = "API is disabled"
	disabledAPICode    = "api_disabled"
)

// isDisabledAPIPath reports whether path belongs to the REST/JSON API surface that
// --disable-api turns off: note CRUD (/notes and everything under it), short-link CRUD
// (/links and everything under it), and the machine-readable contract (API docs, spec,
// llms.txt). The web UI's short note URL (/{id}), /edit/{id}, /r/{id}, and operational
// endpoints (/healthz, /readyz, /static/*) are deliberately not matched here — they keep
// working.
func isDisabledAPIPath(path string) bool {
	return path == "/notes" || strings.HasPrefix(path, "/notes/") ||
		path == "/links" || strings.HasPrefix(path, "/links/") ||
		path == apiDocsPath || path == apiSpecPath || path == llmsTxtPath ||
		// Account provisioning (self-service token issuance, invite minting) is part of the
		// same machine surface: --disable-api is the operator's "headless off" switch, and the
		// middleware blocks them when reached, like the rest of the API.
		path == "/api/tokens" || path == "/api/invites"
}

// withAPIDisabled responds with 503 and a stable code without calling the API handler.
// Outer middleware may respond first; see NewRouter for the current stack order.
// Only wired in when RouterOptions.DisableAPI is set.
func withAPIDisabled(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isDisabledAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)

			return
		}

		w.Header().Set("Content-Type", mimeJSON)
		w.WriteHeader(http.StatusServiceUnavailable)

		err := json.NewEncoder(w).Encode(errorJSON{Message: disabledAPIMessage, Code: disabledAPICode})
		if err != nil {
			log.ErrorContext(r.Context(), "write api-disabled response", "err", err)
		}
	})
}
