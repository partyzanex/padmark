package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

// GetNoteOrLink handles GET /{id...}. One slug names one note, short link or file (the
// slug_namespace table), so the handler reads the note first, then the short link, then the
// file. A short link or file slug never contains "/", so a multi-segment slug is always a note.
// A slug that names none of them gets the same not-found answer as a note read.
func (h *Handler) GetNoteOrLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	note, err := h.NoteHandler.manager.Peek(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) && !strings.Contains(id, "/") {
		link, linkErr := h.followLink(r, id)
		if !errors.Is(linkErr, domain.ErrNotFound) {
			h.respondLink(w, r, id, link, linkErr)

			return
		}

		if h.serveFileIfExists(w, r, id) {
			return
		}
	}

	if err != nil {
		h.writeNoteError(w, r, err)

		return
	}

	h.serveNote(w, r, id, note)
}

// serveFileIfExists answers for the file id and returns true, or returns false when files are
// off or no file has the slug. A browser gets the file page; other clients get the content (see
// wantsFilePage). Peek reads only the record; FilePage and serveFile apply the access rules.
func (h *Handler) serveFileIfExists(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.files.manager == nil {
		return false
	}

	_, err := h.files.manager.Peek(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		return false
	}

	if wantsFilePage(r) {
		h.files.FilePage(w, r, id)

		return true
	}

	h.files.serveFile(w, r, id)

	return true
}
