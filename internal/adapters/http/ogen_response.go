package http

import (
	"log/slog"
	"net/url"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

func domainToResponse(note *domain.Note) *ogenapi.NoteResponse {
	resp := new(ogenapi.NoteResponse)
	resp.ID = note.ID
	resp.Title = note.Title
	resp.Content = note.Content
	resp.ContentType = ogenapi.NoteResponseContentType(derefContentType(note.ContentType))
	resp.Views = note.Views
	resp.BurnAfterReading = note.BurnAfterReading
	resp.CreatedAt = note.CreatedAt
	resp.UpdatedAt = note.UpdatedAt
	resp.Privacy = ogenapi.NewOptNoteResponsePrivacy(ogenapi.NoteResponsePrivacy(note.EffectivePrivacy()))
	// Keep the v0.1.6 response alias through the generated model's accessor.
	resp.SetPrivate(ogenapi.NewOptBool(privateFlag(note)))

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

		return url.URL{
			// Empty url.URL: every component zero. Used as a safe fallback when a stored target URL
			// fails to re-parse — ogen/JSON marshals it as "{}" instead of an erroneous redirect.
			Scheme: "", Opaque: "", User: nil, Host: "", Path: "", Fragment: "",
			RawQuery: "", RawPath: "", RawFragment: "", ForceQuery: false, OmitHost: false,
		}
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
		// ExpiresAt / DirectRedirect populated below when non-nil; zero Opt types otherwise.
		ExpiresAt:      ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: ogenapi.OptNilBool{Value: false, Set: false, Null: false},
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
		// ExpiresAt / DirectRedirect populated below when non-nil; zero Opt types otherwise.
		ExpiresAt:      ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: ogenapi.OptNilBool{Value: false, Set: false, Null: false},
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
	var resp ogenapi.CreateNoteResponse

	resp.ID = note.ID
	resp.Title = note.Title
	resp.Content = note.Content
	resp.ContentType = ogenapi.CreateNoteResponseContentType(derefContentType(note.ContentType))
	resp.EditCode = note.EditCode
	resp.Views = note.Views
	resp.BurnAfterReading = note.BurnAfterReading
	resp.CreatedAt = note.CreatedAt
	resp.UpdatedAt = note.UpdatedAt
	resp.Privacy = ogenapi.NewOptCreateNoteResponsePrivacy(ogenapi.CreateNoteResponsePrivacy(note.EffectivePrivacy()))
	resp.SetPrivate(ogenapi.NewOptBool(privateFlag(note)))

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
	}

	return resp
}
