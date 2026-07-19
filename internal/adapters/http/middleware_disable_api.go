package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// disabledAPIMessage is the fixed body of every REST/JSON API response while --disable-api is
// set. It carries no operational detail on purpose — the API is off, not erroring.
const disabledAPIMessage = "До свидания"

// isDisabledAPIPath reports whether path belongs to the REST/JSON API surface that
// --disable-api turns off: note CRUD (/notes and everything under it) and the API docs/spec.
// The web UI's short note URL (/{id}), /edit/{id}, and operational endpoints (/healthz,
// /readyz, /static/*) are deliberately not matched here — they keep working.
func isDisabledAPIPath(path string) bool {
	return path == "/notes" || strings.HasPrefix(path, "/notes/") ||
		path == apiDocsPath || path == apiSpecPath
}

// withAPIDisabled short-circuits every request under isDisabledAPIPath with a fixed 503 before
// it reaches auth, CSRF, rate-limiting, or any handler — see NewRouter for where this sits in
// the middleware stack. Only wired in when RouterOptions.DisableAPI is set.
func withAPIDisabled(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isDisabledAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)

			return
		}

		w.Header().Set("Content-Type", mimeJSON)
		w.WriteHeader(http.StatusServiceUnavailable)

		err := json.NewEncoder(w).Encode(errorJSON{Message: disabledAPIMessage})
		if err != nil {
			log.ErrorContext(r.Context(), "write api-disabled response", "err", err)
		}
	})
}
