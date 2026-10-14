package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/ogen-go/ogen/validate"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
)

const internalErrorMessage = "internal server error"

// maxBodyErrorMessage is returned with HTTP 413 when a request body exceeds MaxBodyBytes.
const maxBodyErrorMessage = "request body exceeds the server limit"

// decodeErrorMessage is returned with HTTP 400 when a request body cannot be decoded and the
// failure carries no structured detail worth forwarding. Shared with common.writeDecodeError so
// the ogen and native decode paths answer identically.
const decodeErrorMessage = "invalid request body"

// unsupportedMediaTypeMessage prefixes the HTTP 415 reply; the refused Content-Type follows it.
const unsupportedMediaTypeMessage = "unsupported content type"

// notImplementedMessage is returned with HTTP 501 for an operation ogen routed but no handler
// implements — unreachable while every generated operation is implemented, kept so the status
// never falls through to a message describing the wrong thing.
const notImplementedMessage = "not implemented"

// maxEchoedContentType caps how much of the caller's Content-Type is quoted back. The header is
// only bounded by MaxHeaderBytes (64 KiB by default), and echoing it whole would let a caller
// inflate an error response at will.
const maxEchoedContentType = 64

func errResp(err error) ogenapi.ErrorResponse {
	return errorResponse(err.Error())
}

// errorResponse leaves the optional machine-readable code absent for ordinary errors.
func errorResponse(message string) ogenapi.ErrorResponse {
	return ogenapi.ErrorResponse{Message: message, Code: ogenapi.OptString{Value: "", Set: false}}
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
		status := ogenerrors.ErrorCode(err)
		msg := ogenErrorMessage(err, status)

		// Consume the typed result (not the ok flag) so errcheck sees the error-typed return used.
		// Plain assignment (not an if-init) keeps noinlineerr happy.
		maxBytes, _ := errors.AsType[*http.MaxBytesError](err)
		if maxBytes != nil {
			status, msg = http.StatusRequestEntityTooLarge, maxBodyErrorMessage
		}

		// The reply carries only the sanitised message, so the original — byte offsets, generated
		// type names, ogen's decode call chain — goes to the log instead of being lost. Debug for
		// a caller's mistake, error for anything that is the server's.
		if status < http.StatusInternalServerError {
			log.DebugContext(ctx, "ogen rejected request", "status", status, "err", err)
		} else {
			log.ErrorContext(ctx, "ogen request failed", "status", status, "err", err)
		}

		w.Header().Set("Content-Type", mimeJSON)
		w.WriteHeader(status)

		encErr := json.NewEncoder(w).Encode(errorJSON{Message: msg, Code: ""})
		if encErr != nil {
			log.ErrorContext(ctx, "write ogen error response", "status", status, "err", encErr)
		}
	}
}

// ogenErrorMessage renders the client-facing message for a failure ogen raised before the
// request reached a handler method.
//
// ogen's own error strings chain its internals — "operation CreateNote: decode request: decode
// application/json: decode CreateNoteRequest: callback: decode field \"content\": unexpected
// byte 49 '1' at 12" — naming generated types and its decode call path while burying the one
// part a caller can act on. Only the structured details ogen also exposes as typed errors are
// forwarded: which field failed validation, and which Content-Type was refused. Both are already
// published contract. Everything else collapses to a fixed message per status.
func ogenErrorMessage(err error, status int) string {
	contentType, _ := errors.AsType[*validate.InvalidContentTypeError](err)
	if contentType != nil {
		return unsupportedMediaTypeMessage + ": " + truncateForMessage(contentType.ContentType)
	}

	invalid, _ := errors.AsType[*validate.Error](err)
	if invalid != nil && len(invalid.Fields) > 0 {
		return decodeErrorMessage + ": " + fieldErrorsMessage(invalid.Fields)
	}

	switch status {
	case http.StatusBadRequest:
		return decodeErrorMessage
	case http.StatusUnauthorized:
		return msgUnauthorized
	case http.StatusNotImplemented:
		return notImplementedMessage
	default:
		return internalErrorMessage
	}
}

// fieldErrorsMessage lists the failing fields as "<name> (<reason>)". Field names come from the
// schema and the reasons from ogen's validators ("field required", "len 3 less than minimum 5"),
// so every part of this is already something the spec tells the caller.
func fieldErrorsMessage(fields []validate.FieldError) string {
	parts := make([]string, 0, len(fields))

	// Indexed rather than ranged by value: validate.FieldError is wide enough that gocritic's
	// rangeValCopy flags copying it per iteration.
	for i := range fields {
		parts = append(parts, fields[i].Name+" ("+fields[i].Error.Error()+")")
	}

	return strings.Join(parts, ", ")
}

// truncateForMessage bounds a caller-controlled value quoted back in an error body. Cutting on
// runes rather than bytes keeps the result valid UTF-8 for the JSON encoder.
func truncateForMessage(s string) string {
	runes := []rune(s)
	if len(runes) <= maxEchoedContentType {
		return s
	}

	return string(runes[:maxEchoedContentType]) + "…"
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

		v := ogenapi.CreateNoteInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.GetNoteInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.UpdateNoteInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.DeleteNoteInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.CreateLinkInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.GetLinkInternalServerError(errorResponse(internalErrorMessage))

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

		v := ogenapi.DeleteLinkInternalServerError(errorResponse(internalErrorMessage))

		return &v
	}
}
