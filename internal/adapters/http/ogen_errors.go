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

// newOgenErrorHandler builds the handler for every failure ogen raises before a request ever
// reaches a handler method: an oversized body, malformed JSON, a missing required field, a field
// of the wrong type, an unsupported Content-Type.
//
// It replaces ogen's DefaultErrorHandler rather than delegating to it, because that handler
// writes {"error_message": ...} while the spec's ErrorResponse defines message — and defines it
// as required. Delegating therefore produced, on exactly the inputs a client is most likely to
// get wrong, a body missing the one field the contract guarantees. Every response this server
// emits carries {"message": ...}; this was the sole exception.
//
// Status classification is unchanged: ogenerrors.ErrorCode is the same mapping the default
// handler used (400 for a malformed or invalid body, 415 for an unsupported Content-Type, 501
// for an unimplemented operation, 500 otherwise). Only the body-size case is reclassified, from
// the 400 ogen infers for a truncated read to the 413 the limit actually means.
func newOgenErrorHandler(log *slog.Logger) ogenapi.ErrorHandler {
	return func(ctx context.Context, w http.ResponseWriter, _ *http.Request, err error) {
		status, msg := ogenerrors.ErrorCode(err), err.Error()

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
			log.ErrorContext(ctx, "write ogen error response", "status", status, "err", encErr)
		}
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
