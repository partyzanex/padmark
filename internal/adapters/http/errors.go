package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

// errorJSON is the API error body matching the OpenAPI ErrorResponse schema ({"message": ...}).
type errorJSON struct {
	Message string `json:"message"`
}

// errorStatusMessage maps a domain error to the HTTP status and the client-facing message used in
// JSON API error responses.
func errorStatusMessage(err error) (status int, message string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, domain.ErrNotFound.Error()
	case errors.Is(err, domain.ErrExpired):
		return http.StatusGone, domain.ErrExpired.Error()
	case errors.Is(err, domain.ErrTitleTooLong):
		return http.StatusUnprocessableEntity, domain.ErrTitleTooLong.Error()
	case errors.Is(err, domain.ErrInvalidContentType):
		return http.StatusUnprocessableEntity, domain.ErrInvalidContentType.Error()
	case errors.Is(err, domain.ErrInvalidSlug):
		return http.StatusUnprocessableEntity, domain.ErrInvalidSlug.Error()
	case errors.Is(err, domain.ErrCustomSlugDisabled):
		return http.StatusUnprocessableEntity, domain.ErrCustomSlugDisabled.Error()
	case errors.Is(err, domain.ErrSlugConflict):
		return http.StatusConflict, domain.ErrSlugConflict.Error()
	case errors.Is(err, domain.ErrInvalidEditCode):
		return http.StatusForbidden, domain.ErrInvalidEditCode.Error()
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, domain.ErrForbidden.Error()
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// writeDecodeError answers a request whose JSON body could not be decoded: 413 when the body
// exceeded the configured limit (http.MaxBytesReader), otherwise 400. Keeps the native
// multi-segment note handlers consistent with the ogen error surface (JSON ErrorResponse body).
func (h *Handler) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusBadRequest, "invalid request body"

	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
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
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := errorStatusMessage(err)

	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(errorJSON{Message: msg})
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "write error response", "err", encErr)
	}
}
