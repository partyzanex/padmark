package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

// GetNoteOrLink handles GET /{id...}. One slug names one note or one short link (the
// slug_namespace table), so the handler reads the note first and follows the short link only
// when no note has the slug. A short link slug never contains "/", so a multi-segment slug is
// always a note. A slug that names neither gets the same not-found answer as a note read.
func (h *Handler) GetNoteOrLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	note, err := h.NoteHandler.manager.Peek(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) && !strings.Contains(id, "/") {
		link, linkErr := h.followLink(r, id)
		if !errors.Is(linkErr, domain.ErrNotFound) {
			h.respondLink(w, r, id, link, linkErr)

			return
		}
	}

	if err != nil {
		h.writeNoteError(w, r, err)

		return
	}

	h.serveNote(w, r, id, note)
}
