package http

import (
	"net/http"
	"time"
)

// EditPage handles GET /edit/{id} and renders the note editor pre-filled with existing content.
// Gated by the same privacy check as GetNote (handlePrivateAuth) — without it, an authenticated
// caller who is not the note's owner could read a privacy=owner note's plaintext content here,
// bypassing the read restriction that GET /notes/{id} enforces.
func (h *NoteHandler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	note := preloaded
	if note == nil {
		var err error

		note, err = h.manager.Peek(r.Context(), id)
		if err != nil {
			h.writeErrorPage(w, r, err)

			return
		}
	}

	// For burn-after-reading notes the TTL is stored as BurnTTL (a duration set at creation).
	// ExpiresAt is only set after the first read, so we prefer BurnTTL when available.
	// BurnTTL == 0 means immediate burn after reading.
	var ttl int64
	if note.BurnAfterReading || note.BurnTTL > 0 {
		ttl = note.BurnTTL
	} else if note.ExpiresAt != nil {
		if remaining := int64(time.Until(*note.ExpiresAt).Seconds()); remaining > 0 {
			ttl = remaining
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	usr := userFromContext(r)
	isOwner := usr != nil && note.OwnedBy(usr.ID)

	err := h.indexTmpl.Execute(w, editorViewData{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		TTL:              ttl,
		EditMode:         true,
		BurnAfterReading: note.BurnAfterReading || note.BurnTTL > 0,
		Privacy:          string(note.EffectivePrivacy()),
		Nonce:            nonceFromContext(r.Context()),
		IsOwner:          isOwner,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render edit template", "id", id, "err", err)
	}
}
