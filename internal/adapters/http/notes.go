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
	RawContent   string
	Views        int
}

func toNoteViewData(note *domain.Note, rendered string) noteViewData {
	created := note.CreatedAt.Format("Jan 2, 2006, 3:04 PM")

	expires := "Never expires"
	if note.ExpiresAt != nil {
		expires = "Expires " + note.ExpiresAt.Format("Jan 2, 2006")
	}

	return noteViewData{
		ID:           note.ID,
		Title:        note.Title,
		Body:         template.HTML(rendered), //nolint:gosec // content is bluemonday-sanitized
		RawContent:   note.Content,
		CreatedAt:    created,
		Views:        note.Views,
		ExpiresLabel: expires,
	}
}

type noteRequest struct {
	Title            string             `json:"title"`
	Content          string             `json:"content"`
	ContentType      domain.ContentType `json:"content_type,omitempty"`
	Slug             string             `json:"slug,omitempty"`               // custom slug; absent = auto-generated UUID
	TTL              int64              `json:"ttl,omitempty"`                // seconds; 0 or absent means never expires
	BurnAfterReading bool               `json:"burn_after_reading,omitempty"` // delete on first read
}

type noteResponse struct {
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

func toNoteResponse(note *domain.Note) noteResponse {
	return noteResponse{
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

// CreateNote handles POST /notes.
func (h *Handler) CreateNote(w http.ResponseWriter, r *http.Request) {
	var req noteRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var expiresAt *time.Time

	if req.TTL > 0 {
		t := time.Now().Add(time.Duration(req.TTL) * time.Second)
		expiresAt = &t
	}

	note, err := h.manager.Create(r.Context(), &domain.Note{
		ID:               req.Slug,
		Title:            req.Title,
		Content:          req.Content,
		ContentType:      req.ContentType,
		ExpiresAt:        expiresAt,
		BurnAfterReading: req.BurnAfterReading,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "create note", "err", err)
		writeError(w, err)

		return
	}

	w.Header().Set("Content-Type", mimeJSON)
	w.Header().Set("Location", "/notes/"+note.ID)
	w.WriteHeader(http.StatusCreated)

	err = json.NewEncoder(w).Encode(toNoteResponse(note))
	if err != nil {
		h.log.ErrorContext(r.Context(), "encode create response", "err", err)
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *Handler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	switch negotiate(r) {
	case formatHTML:
		note, rendered, err := h.manager.GetRendered(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		err = h.noteTmpl.Execute(w, toNoteViewData(note, rendered))
		if err != nil {
			h.log.ErrorContext(r.Context(), "render note template", "id", id, "err", err)
		}

	case formatPlain:
		note, err := h.manager.View(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}

		ct := "text/plain; charset=utf-8"
		if note.ContentType == domain.ContentTypeMarkdown {
			ct = "text/markdown; charset=utf-8"
		}

		w.Header().Set("Content-Type", ct)

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

		err = json.NewEncoder(w).Encode(toNoteResponse(note))
		if err != nil {
			h.log.ErrorContext(r.Context(), "encode get response", "err", err)
		}
	}
}

// UpdateNote handles PUT /notes/{id}.
func (h *Handler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req noteRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	note, err := h.manager.Update(r.Context(), id, &domain.Note{
		Title:       req.Title,
		Content:     req.Content,
		ContentType: req.ContentType,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "update note", "id", id, "err", err)
		writeError(w, err)

		return
	}

	w.Header().Set("Content-Type", mimeJSON)

	err = json.NewEncoder(w).Encode(toNoteResponse(note))
	if err != nil {
		h.log.ErrorContext(r.Context(), "encode update response", "err", err)
	}
}

// DeleteNote handles DELETE /notes/{id}.
func (h *Handler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	err := h.manager.Delete(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
