package http

import (
	"errors"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

func domainToResponse(note *domain.Note) *ogenapi.NoteResponse {
	resp := &ogenapi.NoteResponse{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      ogenapi.NoteResponseContentType(note.ContentType),
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptDateTime(*note.ExpiresAt)
	}

	return resp
}

func domainToCreateResponse(note *domain.Note) ogenapi.CreateNoteResponse {
	resp := ogenapi.CreateNoteResponse{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      ogenapi.CreateNoteResponseContentType(note.ContentType),
		EditCode:         note.EditCode,
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptDateTime(*note.ExpiresAt)
	}

	return resp
}

func errResp(err error) ogenapi.ErrorResponse {
	return ogenapi.ErrorResponse{Message: err.Error()}
}

func mapCreateError(err error) ogenapi.CreateNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrTitleRequired),
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
		v := ogenapi.CreateNoteInternalServerError(r)

		return &v
	}
}

func mapGetError(err error) ogenapi.GetNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.GetNoteNotFound(r)

		return &v
	case errors.Is(err, domain.ErrExpired):
		v := ogenapi.GetNoteGone(r)

		return &v
	default:
		v := ogenapi.GetNoteInternalServerError(r)

		return &v
	}
}

func mapUpdateError(err error) ogenapi.UpdateNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrForbidden):
		v := ogenapi.UpdateNoteForbidden(r)

		return &v
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.UpdateNoteNotFound(r)

		return &v
	case errors.Is(err, domain.ErrTitleRequired),
		errors.Is(err, domain.ErrInvalidContentType):
		v := ogenapi.UpdateNoteUnprocessableEntity(r)

		return &v
	default:
		v := ogenapi.UpdateNoteInternalServerError(r)

		return &v
	}
}

func mapDeleteError(err error) ogenapi.DeleteNoteRes {
	r := errResp(err)

	switch {
	case errors.Is(err, domain.ErrForbidden):
		v := ogenapi.DeleteNoteForbidden(r)

		return &v
	case errors.Is(err, domain.ErrNotFound):
		v := ogenapi.DeleteNoteNotFound(r)

		return &v
	default:
		v := ogenapi.DeleteNoteInternalServerError(r)

		return &v
	}
}
