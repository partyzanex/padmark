// Package response writes the shared transport-level JSON error format.
package response

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorJSON matches the OpenAPI ErrorResponse schema; Code is optional for existing errors.
type ErrorJSON struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// WriteJSONErrorStatus writes a JSON error response ({"message": msg}) for a status the caller
// already decided on directly, rather than one derived from a domain.error — used by
// middleware whose response describes its own decision (rate limit exceeded, cache lookup
// failed, too many failed edit-code attempts), not a usecase outcome. See writeError for the
// domain-error-driven counterpart with the same wire format (net/http's own http.Error writes
// text/plain, which the OpenAPI ErrorResponse schema never promised).
func WriteJSONErrorStatus(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, msg string) {
	WriteJSONErrorCode(w, r, log, status, msg, "")
}

// WriteJSONErrorCode is WriteJSONErrorStatus plus the optional machine-readable `code` field,
// for decisions a client is expected to branch on rather than show.
func WriteJSONErrorCode(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(ErrorJSON{Message: msg, Code: code})
	if err != nil {
		log.ErrorContext(r.Context(), "write error response", "err", err)
	}
}
