package web

import (
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/link_editor.html
var linkEditorTmplSrc string

//go:embed templates/link.html
var linkTmplSrc string

type linkEditorViewData struct {
	Access         privacyChoice
	ID             string
	TargetURL      string
	Nonce          string
	DirectRedirect string
	ExpiresLabel   string
	Header         headerView
	EditMode       bool
	IsOwner        bool
	BurnAfterClick bool
	DirectAllowed  bool
}

// linkViewData is the data of the page of a short link (link.html).
type linkViewData struct {
	ID string
	// ShortURL is the absolute short link; ShortLabel is the same link without the scheme.
	ShortURL      string
	ShortLabel    string
	TargetURL     string
	CreatedAt     string
	CreatedISO    string
	ExpiresLabel  string
	ExpiresISO    string
	RedirectLabel string
	Privacy       string
	Nonce         string
	Header        headerView
	Views         int
	// BurnAfterClick shows the burn badge; the page itself never burns the link.
	BurnAfterClick bool
	// CanEdit shows the Edit action, with the rule of the note page (renderNoteHTML).
	CanEdit bool
}

// CreateLinkPage renders the short-link form without following any link.
func (h *LinkHandler) CreateLinkPage(w http.ResponseWriter, r *http.Request) {
	header := h.header(r, headerKindLink)
	data := linkEditorViewData{
		Header: header,
		ID:     "", TargetURL: "", Nonce: middleware.NonceFromContext(r.Context()),
		DirectRedirect: "inherit", ExpiresLabel: "", EditMode: false, IsOwner: false, BurnAfterClick: false,
		Access: h.privacyChoice(r, header, domain.PrivacyPublic),
		// A signed-in user becomes the owner of the new link.
		DirectAllowed: h.directRedirect || header.SignedIn(),
	}
	h.renderLinkEditor(w, r, &data)
}

// LinkPage handles GET /links/{id} for a client that prefers HTML: the page of a short link, as
// the note page is the page of a note. GET /{id} stays the redirect, because to follow the short
// link is its function. The page reads the link with Inspect: it does not count a view and does
// not burn a burn_after_click link. It has the privacy and expiry rules of the JSON read
// GET /links/{id}.
func (h *LinkHandler) LinkPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reader := h.reader(r)

	link, err := h.manager.Inspect(r.Context(), id, reader)
	if err != nil {
		h.writeLinkPageError(w, r, err)

		return
	}

	shortURL := h.scheme(r) + "://" + r.Host + "/" + id
	data := linkViewData{
		Header:         h.header(r, headerKindView).forRecord(recordLink),
		ID:             id,
		ShortURL:       shortURL,
		ShortLabel:     r.Host + "/" + id,
		TargetURL:      link.TargetURL,
		CreatedAt:      link.CreatedAt.Format(readerDateLayout),
		CreatedISO:     link.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresLabel:   neverExpiresLabel,
		ExpiresISO:     "",
		RedirectLabel:  "Shows the destination first",
		Privacy:        string(link.EffectivePrivacy()),
		Nonce:          middleware.NonceFromContext(r.Context()),
		Views:          link.Views,
		BurnAfterClick: link.BurnAfterClick,
		// The same rule as the Edit action of the note page (writeNotePage).
		CanEdit: link.VisibleTo(reader),
	}

	if link.EffectiveDirectRedirect(h.directRedirect) {
		data.RedirectLabel = "Opens the destination at once"
	}

	if link.ExpiresAt != nil {
		data.ExpiresLabel = "Expires " + link.ExpiresAt.Format(readerExpiryLayout)
		data.ExpiresISO = link.ExpiresAt.UTC().Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page can show the destination of a private link; a shared cache must not keep it.
	w.Header().Set("Cache-Control", "private, no-store")

	execErr := h.view.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render link page", "id", domain.HashSlug(id), "err", execErr)
	}
}

// editLinkIfExists renders the editor of the short link id and returns true. It returns false
// when no short link has the slug, so that Handler.EditPage can try a file.
func (h *LinkHandler) editLinkIfExists(w http.ResponseWriter, r *http.Request, id string) bool {
	link, err := h.manager.Inspect(r.Context(), id, h.reader(r))
	if errors.Is(err, domain.ErrNotFound) {
		return false
	}

	if err != nil {
		h.writeLinkPageError(w, r, err)

		return true
	}

	h.editLink(w, r, id, link)

	return true
}

// writeLinkPageError answers a link page or a link editor that cannot show the link: the privacy
// answer of common.denyPrivate for a hidden link, the error page for other errors.
func (h *LinkHandler) writeLinkPageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		h.denyPrivate(w, r)

		return
	}

	h.writeErrorPage(w, r, err)
}

// editLink renders the short-link editor of link, which Inspect returned. To open an editor
// never consumes a burn link and never counts a view.
func (h *LinkHandler) editLink(w http.ResponseWriter, r *http.Request, id string, link *domain.Link) {
	header := h.header(r, headerKindPlain)

	data := linkEditorViewData{
		Header: header,
		ID:     id, TargetURL: link.TargetURL, Nonce: middleware.NonceFromContext(r.Context()),
		DirectRedirect: "inherit", ExpiresLabel: neverExpiresLabel, EditMode: true,
		IsOwner: link.OwnedBy(middleware.CallerIDFromCtx(r.Context())), BurnAfterClick: link.BurnAfterClick,
		Access:        h.privacyChoice(r, header, link.EffectivePrivacy()),
		DirectAllowed: h.directRedirect || link.OwnerID != nil,
	}
	if link.DirectRedirect != nil {
		data.DirectRedirect = "false"
		if *link.DirectRedirect {
			data.DirectRedirect = "true"
		}
	}

	if link.ExpiresAt != nil {
		data.ExpiresLabel = "Expires " + link.ExpiresAt.UTC().Format(time.RFC3339)
	}

	h.renderLinkEditor(w, r, &data)
}

// redirectToEditor answers the old editor URLs GET /links/{id}/edit and GET /files/{id}/edit.
// The editor of each record is now at /edit/{id}, as for notes; the redirect keeps old
// bookmarks working.
func redirectToEditor(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/edit/"+url.PathEscape(r.PathValue("id")), http.StatusMovedPermanently)
}

func (h *LinkHandler) renderLinkEditor(w http.ResponseWriter, r *http.Request, data *linkEditorViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.editor.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render link editor", "err", err)
	}
}
