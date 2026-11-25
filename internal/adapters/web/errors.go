package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
)

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
		{domain.ErrContentTooLarge, http.StatusUnprocessableEntity},
		{domain.ErrInvalidContentType, http.StatusUnprocessableEntity},
		{domain.ErrInvalidPrivacy, http.StatusUnprocessableEntity},
		{domain.ErrOwnerPrivacyRequiresOwner, http.StatusUnprocessableEntity},
		{domain.ErrInvalidSlug, http.StatusUnprocessableEntity},
		{domain.ErrCustomSlugDisabled, http.StatusUnprocessableEntity},
		{domain.ErrPrivacyNeedsAccounts, http.StatusUnprocessableEntity},
		// Usually caught earlier by the schema's minLength/maxLength (ogen answers 400); what
		// reaches the usecase (surrounding whitespace, control characters) is a 422 like the
		// other create-time validation errors above.
		{domain.ErrEditCodeTooShort, http.StatusUnprocessableEntity},
		{domain.ErrEditCodeMalformed, http.StatusUnprocessableEntity},
		{domain.ErrSlugConflict, http.StatusConflict},
		{domain.ErrBurnIntentRequired, http.StatusConflict},
		{domain.ErrStepUpRequired, http.StatusForbidden},
		{domain.ErrStepUpFailed, http.StatusForbidden},
		{domain.ErrInvalidAPITokenTTL, http.StatusUnprocessableEntity},
		{domain.ErrInvalidEditCode, http.StatusForbidden},
		{domain.ErrForbidden, http.StatusForbidden},
		// A javascript:, data: or file: target URL is a malformed request, not a valid but
		// unprocessable one, so it gets 400 and not the 422 of the errors above.
		{domain.ErrInvalidURL, http.StatusBadRequest},
		{domain.ErrAPITokenLimit, http.StatusTooManyRequests},
		{domain.ErrFeatureNotSupported, http.StatusServiceUnavailable},
		{domain.ErrFileTooLarge, http.StatusRequestEntityTooLarge},
		{domain.ErrInvalidFileName, http.StatusUnprocessableEntity},
		{domain.ErrStorageUnavailable, http.StatusServiceUnavailable},
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

// writeDecodeError answers a request whose JSON body the handler cannot decode: 413 when the
// body is larger than the configured limit (http.MaxBytesReader), otherwise 400 with a
// constant message. The native handlers with a JSON body (UpdateNoteByPath and
// FileHandler.UpdateFile) call it, so their errors have the ErrorResponse body of the ogen
// handlers. It is a method and not middleware, because only these handlers decode JSON.
func (c *common) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusBadRequest, decodeErrorMessage

	// Consume the typed result (not the ok flag) so errcheck sees the error-typed return used.
	// Plain assignment (not an if-init) keeps noinlineerr happy.
	maxBytes, _ := errors.AsType[*http.MaxBytesError](err)
	if maxBytes != nil {
		status, msg = http.StatusRequestEntityTooLarge, maxBodyErrorMessage
	}

	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(response.ErrorJSON{Message: msg, Code: ""})
	if encErr != nil {
		c.log.ErrorContext(r.Context(), "write decode error response", "err", encErr)
	}
}

// writeError writes a JSON error response ({"message": ...}) matching the OpenAPI ErrorResponse
// schema, so the generated ogen client decodes it into a typed error (e.g. GetNoteNotFound)
// instead of failing on an unexpected content type. Used for non-HTML (API/CLI) clients; browser
// requests get the HTML error page via writeErrorPage / writeNoteError.
func (c *common) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := errorStatusMessage(err)

	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(response.ErrorJSON{Message: msg, Code: ""})
	if encErr != nil {
		c.log.ErrorContext(r.Context(), "write error response", "err", encErr)
	}
}

var errRequestOriginUnavailable = errors.New("request origin unavailable")
