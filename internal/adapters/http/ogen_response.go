package http

import (
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
		Private:          ogenapi.NewOptBool(note.Private),
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
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
		Private:          ogenapi.NewOptBool(note.Private),
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
	}

	return resp
}
