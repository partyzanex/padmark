package web

import (
	"errors"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

// otherRecord answers a request for a slug that a short link or a file has, not a note. The note
// read gives the kind of that record (domain.OtherKindError), so a handler calls only the Manager
// of that kind and makes no other lookup.
type otherRecord func(kind domain.RecordKind)

// answerOther gives the request to other and returns true when err tells that a short link or a
// file has the slug. Otherwise it returns false, and the caller writes err.
func answerOther(err error, other otherRecord) bool {
	held, _ := errors.AsType[*domain.OtherKindError](err)
	if held == nil {
		return false
	}

	other(held.Kind)

	return true
}

// GetNoteOrLink handles GET /{id...}. One slug names one note, short link or file (the
// slug_namespace table). Notes are the most frequent records, so the handler reads the note
// first: a note view needs one query. When no note has the slug, the same query gives the kind of
// the record that has it (notes.Storage.Get), and the handler calls only the Manager of that kind.
// A slug that names no record gets the not-found answer of a note read.
func (h *Handler) GetNoteOrLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	h.serveNote(w, r, id, func(kind domain.RecordKind) {
		switch kind {
		case domain.KindLink:
			h.serveLink(w, r, id)
		case domain.KindFile:
			h.serveFileOrPage(w, r, id)
		default:
			// Storage gives only the kinds above (domain.NoteNotFound); a response is still due.
			h.writeNoteError(w, r, domain.ErrNotFound)
		}
	})
}

// serveLink follows the short link id. A link that the Manager does not find, for example one
// that a parallel request deleted, gets the not-found answer of a slug without a record.
func (h *Handler) serveLink(w http.ResponseWriter, r *http.Request, id string) {
	link, err := h.followLink(r, id)
	if errors.Is(err, domain.ErrNotFound) {
		h.writeNoteError(w, r, err)

		return
	}

	h.respondLink(w, r, id, link, err)
}

// serveFileOrPage answers for the file id. A browser gets the file page; other clients get the
// content (see wantsFilePage). FilePage and serveFile apply the access rules. When files are off,
// a file slug gets the not-found answer of a slug without a record.
func (h *Handler) serveFileOrPage(w http.ResponseWriter, r *http.Request, id string) {
	if h.files.manager == nil {
		h.writeNoteError(w, r, domain.ErrNotFound)

		return
	}

	if wantsFilePage(r) {
		h.files.FilePage(w, r, id)

		return
	}

	h.files.serveFile(w, r, id)
}

// EditPage handles GET /edit/{id...}: the editor of the note, short link or file with this slug,
// so that the three kinds of records have one editor URL. As GetNoteOrLink, it reads the note
// first, and the same query gives the kind of a short link or a file. Each editor reads only the
// record: to open an editor never counts a view and never consumes a burn record. A slug that
// names no record gets the error page of the note editor.
func (h *Handler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	h.editNote(w, r, id, func(kind domain.RecordKind) {
		if !h.editOtherIfExists(w, r, id, kind) {
			h.writeErrorPage(w, r, domain.ErrNotFound)
		}
	})
}

// editOtherIfExists opens the editor of the short link or the file id and returns true. It
// returns false when the Manager of kind does not find the record, or when files are off.
func (h *Handler) editOtherIfExists(w http.ResponseWriter, r *http.Request, id string, kind domain.RecordKind) bool {
	switch kind {
	case domain.KindLink:
		return h.editLinkIfExists(w, r, id)
	case domain.KindFile:
		return h.files.editFileIfExists(w, r, id)
	default:
		// Storage gives only the kinds above (domain.NoteNotFound).
		return false
	}
}
