package http

import (
	"log/slog"
	"net/url"
	"time"

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
		Private:          ogenapi.NewOptBool(privateFlag(note)), //nolint:staticcheck // deprecated alias emitted on purpose
		// ExpiresAt populated below when note.ExpiresAt is non-nil; zero OptNilDateTime otherwise.
		ExpiresAt: ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
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
		Private: ogenapi.NewOptBool(privateFlag(note)), //nolint:staticcheck // deprecated alias emitted on purpose
		// ExpiresAt populated below when note.ExpiresAt is non-nil; zero OptNilDateTime otherwise.
		ExpiresAt: ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
	}

	if note.ExpiresAt != nil {
		resp.ExpiresAt = ogenapi.NewOptNilDateTime(*note.ExpiresAt)
	}

	return resp
}
