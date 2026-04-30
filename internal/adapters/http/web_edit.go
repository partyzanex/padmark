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

	// For burn-after-reading notes the TTL is stored as BurnTTL (a duration set at creation).
	// ExpiresAt is only set after the first read, so we prefer BurnTTL when available.
	var ttl int64
	if note.BurnTTL > 0 {
		ttl = note.BurnTTL
	} else if note.ExpiresAt != nil {
		if remaining := int64(time.Until(*note.ExpiresAt).Seconds()); remaining > 0 {
			ttl = remaining
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err = h.indexTmpl.Execute(w, editorViewData{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		TTL:              ttl,
		EditMode:         true,
		BurnAfterReading: note.BurnAfterReading || note.BurnTTL > 0,
		Private:          note.Private,
		Nonce:            nonceFromContext(r.Context()),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render edit template", "id", id, "err", err)
	}
}
