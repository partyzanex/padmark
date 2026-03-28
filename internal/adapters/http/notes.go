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

var noteTmpl = template.Must(template.New("note").Parse(noteTmplSrc))

type noteRequest struct {
	Title       string             `json:"title"`
	Content     string             `json:"content"`
	ContentType domain.ContentType `json:"content_type,omitempty"`
}

type noteResponse struct {
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Content     string             `json:"content"`
	ContentType domain.ContentType `json:"content_type"`
}

func toNoteResponse(n *domain.Note) noteResponse {
	return noteResponse{
		ID:          n.ID,
		Title:       n.Title,
		Content:     n.Content,
		ContentType: n.ContentType,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}
}

// CreateNote handles POST /notes.
func (h *Handler) CreateNote(w http.ResponseWriter, r *http.Request) {
	var req noteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	note, err := h.manager.Create(r.Context(), &domain.Note{
		Title:       req.Title,
		Content:     req.Content,
		ContentType: req.ContentType,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "create note", "err", err)
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", mimeJSON)
	w.Header().Set("Location", "/notes/"+note.ID)
	w.WriteHeader(http.StatusCreated)

	if err = json.NewEncoder(w).Encode(toNoteResponse(note)); err != nil {
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

		if err = noteTmpl.Execute(w, struct {
			Title string
			Body  template.HTML
		}{Title: note.Title, Body: template.HTML(rendered)}); err != nil {
			h.log.ErrorContext(r.Context(), "render note template", "id", id, "err", err)
		}

	case formatPlain:
		note, err := h.manager.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, note.Content)

	default:
		note, err := h.manager.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}

		w.Header().Set("Content-Type", mimeJSON)

		if err = json.NewEncoder(w).Encode(toNoteResponse(note)); err != nil {
			h.log.ErrorContext(r.Context(), "encode get response", "err", err)
		}
	}
}

// UpdateNote handles PUT /notes/{id}.
func (h *Handler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req noteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	if err = json.NewEncoder(w).Encode(toNoteResponse(note)); err != nil {
		h.log.ErrorContext(r.Context(), "encode update response", "err", err)
	}
}

// DeleteNote handles DELETE /notes/{id}.
func (h *Handler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.manager.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
