package http

import (
	"net/http"
	"time"
)

// EditPage handles GET /edit/{id} and renders the note editor pre-filled with existing content.
func (h *Handler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	note, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)
		return
	}

	var ttl int64

	if note.ExpiresAt != nil {
		remaining := time.Until(*note.ExpiresAt).Seconds()
		if remaining > 0 {
			ttl = int64(remaining)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err = h.indexTmpl.Execute(w, editorViewData{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		TTL:              ttl,
		EditMode:         true,
		BurnAfterReading: note.BurnAfterReading,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render edit template", "id", id, "err", err)
	}
}
