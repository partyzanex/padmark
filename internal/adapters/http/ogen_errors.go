package http

import (
	"errors"
	"log/slog"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

const internalErrorMessage = "internal server error"

func errResp(err error) ogenapi.ErrorResponse {
	return ogenapi.ErrorResponse{Message: err.Error()}
}

//nolint:ireturn // ogen response union types are interfaces by design
func mapCreateError(err error, log *slog.Logger) ogenapi.CreateNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrTitleRequired),
		errors.Is(err, domain.ErrTitleTooLong),
		errors.Is(err, domain.ErrInvalidContentType),
		errors.Is(err, domain.ErrInvalidSlug):
		v := ogenapi.CreateNoteUnprocessableEntity(r)

		return &v
	case errors.Is(err, domain.ErrContentTooLong):
		v := ogenapi.CreateNoteRequestEntityTooLarge(r)

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
	case errors.Is(err, domain.ErrForbidden):
		v := ogenapi.UpdateNoteForbidden(r)

		return &v
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.UpdateNoteNotFound(r)

		return &v
	case errors.Is(err, domain.ErrTitleRequired),
		errors.Is(err, domain.ErrTitleTooLong),
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
	case errors.Is(err, domain.ErrForbidden):
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
