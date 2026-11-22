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

	h.serveNote(w, r, id, h.otherRecord(id, func() bool {
		link, linkErr := h.followLink(r, id)
		if errors.Is(linkErr, domain.ErrNotFound) {
			return h.serveFileIfExists(w, r, id)
		}

		h.respondLink(w, r, id, link, linkErr)

		return true
	}))
}

// otherRecord returns answer as the missing fallback of a note read, or nil for a multi-segment
// slug, which only a note can have.
func (*Handler) otherRecord(id string, answer func() bool) func() bool {
	if strings.Contains(id, "/") {
		return nil
	}

	return answer
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

// EditPage handles GET /edit/{id...}: the editor of the note, short link or file with this slug,
// so that the three kinds of records have one editor URL. It reads the records in the order of
// GetNoteOrLink: the note, then the short link, then the file. A short link or file slug never
// contains "/", so a multi-segment slug is always a note. Each editor reads only the record: to
// open an editor never counts a view and never consumes a burn record.
func (h *Handler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	h.editNote(w, r, id, h.otherRecord(id, func() bool {
		return h.editLinkIfExists(w, r, id) || h.files.editFileIfExists(w, r, id)
	}))
}
