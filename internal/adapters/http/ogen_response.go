package http

import (
	"log/slog"
	"net/url"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

func domainToResponse(note *domain.Note) *ogenapi.NoteResponse {
	resp := &ogenapi.NoteResponse{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      ogenapi.NoteResponseContentType(derefContentType(note.ContentType)),
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
		Privacy:          ogenapi.NewOptNoteResponsePrivacy(ogenapi.NoteResponsePrivacy(note.EffectivePrivacy())),
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
	}

	return resp
}

// linkTargetURL parses link.TargetURL for an ogen response field. A parse failure is not
// expected in practice — the stored value already round-tripped through url.Parse once in
// links.Manager's validateTargetURL, and decryption failures are collapsed to domain.ErrNotFound
// before a *domain.Link ever reaches this layer — but is logged and degrades to an empty URL
// rather than panicking, since a response mapper has no error return to propagate one through.
func linkTargetURL(raw string, log *slog.Logger) url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		log.Error("parse stored link target url", slog.String("error", err.Error()))

		return url.URL{}
	}

	return *parsed
}

func domainLinkToResponse(link *domain.Link, log *slog.Logger) *ogenapi.LinkResponse {
	resp := &ogenapi.LinkResponse{
		ID:             link.ID,
		TargetURL:      linkTargetURL(link.TargetURL, log),
		Views:          link.Views,
		BurnAfterClick: link.BurnAfterClick,
		CreatedAt:      link.CreatedAt,
		Privacy:        ogenapi.NewOptLinkResponsePrivacy(ogenapi.LinkResponsePrivacy(link.EffectivePrivacy())),
	}

	if link.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*link.ExpiresAt)
	}

	if link.DirectRedirect != nil {
		resp.DirectRedirect = ogenapi.NewOptNilBool(*link.DirectRedirect)
	}

	return resp
}

func domainLinkToCreateResponse(link *domain.Link, log *slog.Logger) ogenapi.CreateLinkResponse {
	resp := ogenapi.CreateLinkResponse{
		ID:             link.ID,
		TargetURL:      linkTargetURL(link.TargetURL, log),
		EditCode:       link.EditCode,
		Views:          link.Views,
		BurnAfterClick: link.BurnAfterClick,
		CreatedAt:      link.CreatedAt,
		Privacy: ogenapi.NewOptCreateLinkResponsePrivacy(
			ogenapi.CreateLinkResponsePrivacy(link.EffectivePrivacy()),
		),
	}

	if link.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*link.ExpiresAt)
	}

	if link.DirectRedirect != nil {
		resp.DirectRedirect = ogenapi.NewOptNilBool(*link.DirectRedirect)
	}

	return resp
}

func domainToCreateResponse(note *domain.Note) ogenapi.CreateNoteResponse {
	resp := ogenapi.CreateNoteResponse{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      ogenapi.CreateNoteResponseContentType(derefContentType(note.ContentType)),
		EditCode:         note.EditCode,
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
		Privacy: ogenapi.NewOptCreateNoteResponsePrivacy(
			ogenapi.CreateNoteResponsePrivacy(note.EffectivePrivacy()),
		),
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
	}

	return resp
}
