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
}

func toNoteJSON(note *domain.Note) noteJSON {
	return noteJSON{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      note.ContentType,
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
		ExpiresAt:        note.ExpiresAt,
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *Handler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	switch negotiate(r) {
	case formatHTML:
		note, rendered, err := h.manager.GetRendered(r.Context(), id)
		if err != nil {
			h.writeErrorPage(w, r, err)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		data := toNoteViewData(note, rendered)
		data.Nonce = nonceFromContext(r.Context())

		err = h.noteTmpl.Execute(w, data)
		if err != nil {
			h.log.ErrorContext(r.Context(), "render note template", "id", id, "err", err)
		}

	case formatPlain:
		note, err := h.manager.View(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}

		ext := ".txt"
		ct := "text/plain; charset=utf-8"

		if note.ContentType == domain.ContentTypeMarkdown {
			ct = "text/markdown; charset=utf-8"
			ext = ".md"
		}

		hdr := w.Header()
		hdr.Set("Content-Type", ct)

		// When the client explicitly requests raw content via ?raw=1, force a
		// download so that browsers and CDNs do not render or cache the file inline.
		if r.URL.Query().Get("raw") == "1" {
			hdr.Set("Content-Disposition", "attachment; filename=\""+id+ext+"\"")
		}

		_, err = fmt.Fprint(w, note.Content) //nolint:gosec // raw content is intentional for text/plain and text/markdown
		if err != nil {
			h.log.ErrorContext(r.Context(), "write plain content", "id", id, "err", err)
		}

	default:
		note, err := h.manager.View(r.Context(), id)
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
