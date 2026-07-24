package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ogen-go/ogen/ogenerrors"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
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

// mapCreateError, mapGetError, mapUpdateError, and mapDeleteError each switch on the status from
// the shared domainErrStatus table (errors.go) rather than repeating their own errors.Is chain.
// The switch itself can't be unified further: ogen generates a distinct response-union type per
// operation (CreateNoteRes, GetNoteRes, ...), so each function still has to name its own
// operation-specific variant constructor for every status it can produce.

//nolint:ireturn // ogen response union types are interfaces by design
func mapCreateError(err error, log *slog.Logger) ogenapi.CreateNoteRes {
	r := errResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusUnprocessableEntity:
		v := ogenapi.CreateNoteUnprocessableEntity(r)

		return &v
	case http.StatusConflict:
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

	switch status, _ := domainErrStatus(err); status {
	case http.StatusNotFound:
		v := ogenapi.GetNoteNotFound(r)

		return &v
	case http.StatusGone:
		v := ogenapi.GetNoteGone(r)

		return &v
	default:
		log.Error("get note failed", slog.String("error", err.Error()))

		v := ogenapi.GetNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

// mapUpdateError is structurally similar to mapCreateLinkError (both have 3 status cases +
// default) but each names its own operation-specific variant constructor — ogen generates a
// distinct type per operation, so this can't be unified further without generics obscuring more
// than they'd save for 2 call sites.
//
//nolint:ireturn,dupl // ogen response union types are interfaces by design; see comment above
func mapUpdateError(err error, log *slog.Logger) ogenapi.UpdateNoteRes {
	r := errResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusForbidden:
		v := ogenapi.UpdateNoteForbidden(r)

		return &v
	case http.StatusNotFound:
		v := ogenapi.UpdateNoteNotFound(r)

		return &v
	case http.StatusUnprocessableEntity:
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

	switch status, _ := domainErrStatus(err); status {
	case http.StatusForbidden:
		v := ogenapi.DeleteNoteForbidden(r)

		return &v
	case http.StatusNotFound:
		v := ogenapi.DeleteNoteNotFound(r)

		return &v
	default:
		log.Error("delete note failed", slog.String("error", err.Error()))

		v := ogenapi.DeleteNoteInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

// mapCreateLinkError, mapGetLinkError, and mapDeleteLinkError mirror mapCreateError/mapGetError/
// mapDeleteError, for the links operations' own distinct ogen response-union types. See
// mapUpdateError's comment for why mapCreateLinkError's structural similarity to it isn't
// unified further.
//
//nolint:ireturn,dupl // ogen response union types are interfaces by design; see comment above
func mapCreateLinkError(err error, log *slog.Logger) ogenapi.CreateLinkRes {
	r := errResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusBadRequest:
		v := ogenapi.CreateLinkBadRequest(r)

		return &v
	case http.StatusUnprocessableEntity:
		v := ogenapi.CreateLinkUnprocessableEntity(r)

		return &v
	case http.StatusConflict:
		v := ogenapi.CreateLinkConflict(r)

		return &v
	default:
		log.Error("create link failed", slog.String("error", err.Error()))

		v := ogenapi.CreateLinkInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapGetLinkError(err error, log *slog.Logger) ogenapi.GetLinkRes {
	r := errResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusNotFound:
		v := ogenapi.GetLinkNotFound(r)

		return &v
	case http.StatusGone:
		v := ogenapi.GetLinkGone(r)

		return &v
	default:
		log.Error("get link failed", slog.String("error", err.Error()))

		v := ogenapi.GetLinkInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapDeleteLinkError(err error, log *slog.Logger) ogenapi.DeleteLinkRes {
	r := errResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusForbidden:
		v := ogenapi.DeleteLinkForbidden(r)

		return &v
	case http.StatusNotFound:
		v := ogenapi.DeleteLinkNotFound(r)

		return &v
	default:
		log.Error("delete link failed", slog.String("error", err.Error()))

		v := ogenapi.DeleteLinkInternalServerError(ogenapi.ErrorResponse{Message: internalErrorMessage})

		return &v
	}
}
