package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/note.html
var noteTmplSrc string

type noteViewData struct {
	Body         template.HTML
	Title        string
	ID           string
	CreatedAt    string
	ExpiresLabel string
	ExpiresISO   string // RFC 3339 timestamp for JS client-side formatting; empty when never expires
	RawContent   string
	Nonce        string
	Views        int
	Private      bool
	CanEdit      bool
}

func toNoteViewData(note *domain.Note, rendered string) noteViewData {
	created := note.CreatedAt.Format("Jan 2, 2006, 3:04 PM")

	expires := "Never expires"
	expiresISO := ""

	if note.ExpiresAt != nil {
		expires = "Expires " + note.ExpiresAt.Format("Jan 2, 2006")
		expiresISO = note.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return noteViewData{
		ID:           note.ID,
		Title:        note.Title,
		Body:         template.HTML(rendered), //nolint:gosec // content is bluemonday-sanitized
		RawContent:   note.Content,
		CreatedAt:    created,
		Views:        note.Views,
		ExpiresLabel: expires,
		ExpiresISO:   expiresISO,
		Private:      note.Private != nil && *note.Private,
	}
}

type noteJSON struct {
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	ExpiresAt        *time.Time         `json:"expires_at"`
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	Content          string             `json:"content"`
	ContentType      domain.ContentType `json:"content_type"`
	Views            int                `json:"views"`
	BurnAfterReading bool               `json:"burn_after_reading"`
	Private          bool               `json:"private"`
}

func toNoteJSON(note *domain.Note) noteJSON {
	return noteJSON{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      derefContentType(note.ContentType),
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
		ExpiresAt:        note.ExpiresAt,
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		Private:          note.Private != nil && *note.Private,
	}
}

// handlePrivateAuth checks whether a note is private and the caller is authenticated.
// Returns the preloaded note (when auth is configured) and false when the request may proceed.
// Returns nil and true when the response has been written (auth failed or note not found).
func (h *Handler) handlePrivateAuth(w http.ResponseWriter, r *http.Request, id string) (*domain.Note, bool) {
	if h.allowedTokens == nil {
		return nil, false
	}

	note, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return nil, true
	}

	if (note.Private == nil || !*note.Private) || h.isAuthenticated(r) {
		return note, false
	}

	if negotiate(r) == formatHTML {
		http.Redirect(w, r, "/login", http.StatusSeeOther)

		return nil, true
	}

	http.Error(w, "unauthorized", http.StatusUnauthorized)

	return nil, true
}

func (h *Handler) renderNoteHTML(w http.ResponseWriter, r *http.Request, id string, preloaded *domain.Note) {
	var (
		note     *domain.Note
		rendered string
		err      error
	)

	if preloaded != nil {
		note, rendered, err = h.manager.GetRenderedPreloaded(r.Context(), id, preloaded)
	} else {
		note, rendered, err = h.manager.GetRendered(r.Context(), id)
	}

	if err != nil {
		h.writeErrorPage(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := toNoteViewData(note, rendered)
	data.Nonce = nonceFromContext(r.Context())
	data.CanEdit = h.allowedTokens != nil && h.isAuthenticated(r)

	err = h.noteTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render note template", "id", id, "err", err)
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *Handler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check auth for private notes before any side effects (view count, burn).
	// When auth is configured, preloaded holds the already-fetched note so the
	// subsequent View/GetRendered can skip a second SELECT.
	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	switch negotiate(r) {
	case formatHTML:
		h.renderNoteHTML(w, r, id, preloaded)

	case formatPlain:
		note, err := h.viewNote(r, id, preloaded)
		if err != nil {
			writeError(w, err)
			return
		}

		contentType := "text/plain; charset=utf-8"

		if note.ContentType == nil || *note.ContentType == domain.ContentTypeMarkdown {
			contentType = "text/markdown; charset=utf-8"
		}

		hdr := w.Header()
		hdr.Set("Content-Type", contentType)

		_, err = fmt.Fprint(w, note.Content) // raw content is intentional for text/plain and text/markdown
		if err != nil {
			h.log.ErrorContext(r.Context(), "write plain content", "id", id, "err", err)
		}

	default:
		note, err := h.viewNote(r, id, preloaded)
		if err != nil {
			writeError(w, err)
			return
		}

		w.Header().Set("Content-Type", mimeJSON)

		err = json.NewEncoder(w).Encode(toNoteJSON(note))
		if err != nil {
			h.log.ErrorContext(r.Context(), "encode get response", "err", err)
		}
	}
}

func (h *Handler) viewNote(r *http.Request, id string, preloaded *domain.Note) (*domain.Note, error) {
	if preloaded != nil {
		note, err := h.manager.ViewPreloaded(r.Context(), id, preloaded)
		if err != nil {
			return nil, fmt.Errorf("view note: %w", err)
		}

		return note, nil
	}

	note, err := h.manager.View(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("view note: %w", err)
	}

	return note, nil
}
