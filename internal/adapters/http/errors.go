package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

// errorJSON is the API error body matching the OpenAPI ErrorResponse schema ({"message": ...}).
type errorJSON struct {
	Message string `json:"message"`
}

// writeJSONErrorStatus writes a JSON error response ({"message": msg}) for a status the caller
// already decided on directly, rather than one derived from a domain.error — used by
// middleware whose response describes its own decision (rate limit exceeded, cache lookup
// failed, too many failed edit-code attempts), not a usecase outcome. See writeError for the
// domain-error-driven counterpart with the same wire format (net/http's own http.Error writes
// text/plain, which the OpenAPI ErrorResponse schema never promised).
func writeJSONErrorStatus(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, msg string) {
	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(errorJSON{Message: msg})
	if err != nil {
		log.ErrorContext(r.Context(), "write error response", "err", err)
	}
}

// domainErrStatus maps a domain error to its canonical HTTP status code and the matched
// sentinel's short client-facing message — "" when err doesn't match anything known (message,
// not the sentinel itself, so callers that only need the status can discard it via `_` without
// tripping errcheck's blank-error-discard check).
//
// This is the single source of truth for "what status does this error deserve": errorStatusMessage
// (JSON API errors), domainErrToPageData (HTML error page), and the ogen response mappers
// (ogen_errors.go) all derive their status from this table instead of each repeating the same
// errors.Is chain. A slice (checked in order) rather than a switch keeps this a flat,
// O(1)-complexity lookup as entries are added — none of these sentinels wrap one another, so
// match order never affects the result.
func domainErrStatus(err error) (status int, message string) {
	entries := []struct {
		err    error
		status int
	}{
		{domain.ErrNotFound, http.StatusNotFound},
		{domain.ErrExpired, http.StatusGone},
		{domain.ErrTitleTooLong, http.StatusUnprocessableEntity},
		{domain.ErrInvalidContentType, http.StatusUnprocessableEntity},
		{domain.ErrInvalidPrivacy, http.StatusUnprocessableEntity},
		{domain.ErrOwnerPrivacyRequiresOwner, http.StatusUnprocessableEntity},
		{domain.ErrInvalidSlug, http.StatusUnprocessableEntity},
		{domain.ErrCustomSlugDisabled, http.StatusUnprocessableEntity},
		{domain.ErrSlugConflict, http.StatusConflict},
		{domain.ErrInvalidEditCode, http.StatusForbidden},
		{domain.ErrForbidden, http.StatusForbidden},
		// javascript:/data:/file:/etc. target URLs are a malformed request, not an
		// otherwise-valid-but-unprocessable one — matches the acceptance criteria's literal 400
		// (see TODO.md's Bug-class checklist, old C4), unlike the 422s above.
		{domain.ErrInvalidURL, http.StatusBadRequest},
	}

	for _, entry := range entries {
		if errors.Is(err, entry.err) {
			return entry.status, entry.err.Error()
		}
	}

	return http.StatusInternalServerError, ""
}

// errorStatusMessage maps a domain error to the HTTP status and the client-facing message used in
// JSON API error responses.
func errorStatusMessage(err error) (status int, message string) {
	status, message = domainErrStatus(err)
	if message == "" {
		return status, "internal server error"
	}

	return status, message
}

// writeDecodeError answers a request whose JSON body could not be decoded: 413 when the body
// exceeded the configured limit (http.MaxBytesReader), otherwise 400. Keeps the native
// multi-segment note handlers consistent with the ogen error surface (JSON ErrorResponse body).
// Stays a single small method rather than shared middleware since it has one caller today
// (UpdateNoteByPath); future native handlers with a JSON body should call this method directly.
func (h *common) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusBadRequest, decodeErrorMessage

	// Consume the typed result (not the ok flag) so errcheck sees the error-typed return used.
	// Plain assignment (not an if-init) keeps noinlineerr happy.
	maxBytes, _ := errors.AsType[*http.MaxBytesError](err)
	if maxBytes != nil {
		status, msg = http.StatusRequestEntityTooLarge, maxBodyErrorMessage
	}

	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(errorJSON{Message: msg})
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "write decode error response", "err", encErr)
	}
}

// writeError writes a JSON error response ({"message": ...}) matching the OpenAPI ErrorResponse
// schema, so the generated ogen client decodes it into a typed error (e.g. GetNoteNotFound)
// instead of failing on an unexpected content type. Used for non-HTML (API/CLI) clients; browser
// requests get the HTML error page via writeErrorPage / writeNoteError.
func (h *common) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := errorStatusMessage(err)

	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(errorJSON{Message: msg})
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "write error response", "err", encErr)
	}
}
