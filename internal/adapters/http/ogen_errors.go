package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ogen-go/ogen/ogenerrors"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

const internalErrorMessage = "internal server error"

// maxBodyErrorMessage is returned with HTTP 413 when a request body exceeds MaxBodyBytes.
const maxBodyErrorMessage = "request body exceeds the server limit"

func errResp(err error) ogenapi.ErrorResponse {
	return ogenapi.ErrorResponse{Message: err.Error()}
}

// newMaxBodyErrorHandler builds an ogen error handler that intercepts request-body-size violations
// (http.MaxBytesReader, wired via withBodyLimit from MaxBodyBytes) surfacing during ogen request
// decoding, and answers with a clean 413 matching the OpenAPI ErrorResponse schema — so an
// oversized note body reads as a documented "request entity too large" rather than an opaque
// decode error. Every other error falls through to ogen's default handler.
func newMaxBodyErrorHandler(log *slog.Logger) ogenapi.ErrorHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.Header().Set("Content-Type", mimeJSON)
			w.WriteHeader(http.StatusRequestEntityTooLarge)

			encErr := json.NewEncoder(w).Encode(errorJSON{Message: maxBodyErrorMessage})
			if encErr != nil {
				log.ErrorContext(ctx, "write 413 response", "err", encErr)
			}

			return
		}

		ogenerrors.DefaultErrorHandler(ctx, w, r, err)
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapCreateError(err error, log *slog.Logger) ogenapi.CreateNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrTitleTooLong),
		errors.Is(err, domain.ErrInvalidContentType),
		errors.Is(err, domain.ErrInvalidSlug):
		v := ogenapi.CreateNoteUnprocessableEntity(r)

		return &v
	case errors.Is(err, domain.ErrSlugConflict):
		v := ogenapi.CreateNoteConflict(r)

		return &v
	default:
		log.Error("create note failed", slog.String("error", err.Error()))

		v := ogenapi.CreateNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapGetError(err error, log *slog.Logger) ogenapi.GetNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.GetNoteNotFound(r)

		return &v
	case errors.Is(err, domain.ErrExpired):
		v := ogenapi.GetNoteGone(r)

		return &v
	default:
		log.Error("get note failed", slog.String("error", err.Error()))

		v := ogenapi.GetNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapUpdateError(err error, log *slog.Logger) ogenapi.UpdateNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrInvalidEditCode), errors.Is(err, domain.ErrForbidden):
		v := ogenapi.UpdateNoteForbidden(r)

		return &v
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.UpdateNoteNotFound(r)

		return &v
	case errors.Is(err, domain.ErrTitleTooLong),
		errors.Is(err, domain.ErrInvalidContentType):
		v := ogenapi.UpdateNoteUnprocessableEntity(r)

		return &v
	default:
		log.Error("update note failed", slog.String("error", err.Error()))

		v := ogenapi.UpdateNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapDeleteError(err error, log *slog.Logger) ogenapi.DeleteNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrInvalidEditCode), errors.Is(err, domain.ErrForbidden):
		v := ogenapi.DeleteNoteForbidden(r)

		return &v
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.DeleteNoteNotFound(r)

		return &v
	default:
		log.Error("delete note failed", slog.String("error", err.Error()))

		v := ogenapi.DeleteNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}
