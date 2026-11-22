//go:generate go run go.uber.org/mock/mockgen@latest -source=note_or_link.go -destination=note_or_link_mocks_test.go -package=web_test

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// errUnknownSlugKind tells that the resolver gave a kind that the handler does not serve. The
// resolver returns only the three kinds (slugs.Resolver.KindOf), so this error is a code fault.
var errUnknownSlugKind = errors.New("unknown record kind")

// SlugResolver tells which kind of record has a slug (slugs.Resolver). It returns
// domain.ErrNotFound when no record has the slug. Other errors are fail-hard: the handler
// answers with the error and reads no record.
type SlugResolver interface {
	KindOf(ctx context.Context, slug string) (slugs.Kind, error)
}

// GetNoteOrLink handles GET /{id...}. One slug names one note, short link or file (the
// slug_namespace table), so the handler asks the resolver for the kind and then calls only the
// Manager of that kind. A slug that names no record gets the not-found answer of a note read.
func (h *Handler) GetNoteOrLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	kind, err := h.slugs.KindOf(r.Context(), id)
	if err != nil {
		h.writeNoteError(w, r, err)

		return
	}

	switch kind {
	case slugs.KindNote:
		h.serveNote(w, r, id)
	case slugs.KindLink:
		h.serveLink(w, r, id)
	case slugs.KindFile:
		h.serveFileOrPage(w, r, id)
	default:
		h.writeNoteError(w, r, fmt.Errorf("slug kind %q: %w", kind, errUnknownSlugKind))
	}
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
// so that the three kinds of records have one editor URL. The resolver gives the kind, as for
// GetNoteOrLink. Each editor reads only the record: to open an editor never counts a view and
// never consumes a burn record. A slug that names no record gets the error page of the note
// editor.
func (h *Handler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	kind, err := h.slugs.KindOf(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	switch kind {
	case slugs.KindNote:
		h.editNote(w, r, id)
	case slugs.KindLink:
		if !h.editLinkIfExists(w, r, id) {
			h.writeErrorPage(w, r, domain.ErrNotFound)
		}
	case slugs.KindFile:
		if !h.files.editFileIfExists(w, r, id) {
			h.writeErrorPage(w, r, domain.ErrNotFound)
		}
	default:
		h.writeErrorPage(w, r, fmt.Errorf("slug kind %q: %w", kind, errUnknownSlugKind))
	}
}
