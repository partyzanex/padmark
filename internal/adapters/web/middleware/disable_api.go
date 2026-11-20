package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/adapters/web/response"
)

// disabledAPIMessage is the fixed body of every REST/JSON API response while --disable-api is
// set. It carries no operational detail on purpose — the API is off, not erroring.
const (
	disabledAPIMessage = "API is disabled"
	disabledAPICode    = "api_disabled"
)

// isDisabledAPIPath reports whether path belongs to the REST/JSON API surface that
// --disable-api turns off: note CRUD (/notes and everything under it), short-link CRUD
// (/links and everything under it), file upload and management (/files and everything under
// it), and the machine-readable contract (API docs, spec,
// llms.txt). The web UI's /{id} (note, short link or file), /edit/{id}, /r/{id}, and operational
// endpoints (/healthz, /readyz, /static/*) are deliberately not matched here — they keep
// working.
func isDisabledAPIPath(path string) bool {
	for _, root := range []string{"/notes", "/links", "/files"} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}

	return path == apiDocsPath || path == apiSpecPath || path == llmsTxtPath ||
		path == apiReferencePath ||
		// Account provisioning (self-service token issuance, invite minting) is part of the
		// same machine surface: --disable-api is the operator's "headless off" switch, and the
		// middleware blocks them when reached, like the rest of the API.
		path == "/api/tokens" || path == "/api/invites"
}

// WithAPIDisabled responds with 503 and a stable code without calling the API handler.
// Outer middleware may respond first; see NewRouter for the current stack order.
// Only wired in when RouterOptions.DisableAPI is set.
func WithAPIDisabled(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isDisabledAPIPath(r.URL.Path) || isFilePage(r) {
			next.ServeHTTP(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)

		err := json.NewEncoder(w).Encode(response.ErrorJSON{Message: disabledAPIMessage, Code: disabledAPICode})
		if err != nil {
			log.ErrorContext(r.Context(), "write api-disabled response", "err", err)
		}
	})
}

// isFilePage reports whether r reads a browser page of the file feature: the upload form
// GET /files, the edit form GET /files/{id}/edit and the download button GET
// /files/{id}/download. They are web UI like /{id}, so --disable-api keeps them. The forms
// then only show that upload, change and delete are off.
func isFilePage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	if r.URL.Path == "/files" {
		return true
	}

	rest, found := strings.CutPrefix(r.URL.Path, "/files/")
	if !found {
		return false
	}

	slug, action, found := strings.Cut(rest, "/")

	return found && slug != "" && (action == "edit" || action == "download")
}
