package web

import (
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/redirect.html
var redirectTmplSrc string

// LinkHandler serves the browser's link page, link editor and redirect routes. OgenHandler
// serves the JSON API, mirroring the split between note pages and note API operations.
type LinkHandler struct {
	*common

	manager LinkManager
	tmpl    *template.Template
	editor  *template.Template
	// view is the page of a short link (link.html), the counterpart of the note page.
	view *template.Template

	// directRedirect selects the short-link redirect's response mode: true issues an immediate 301 to the
	// target URL, false renders the interstitial (redirect.html). Set via Handler.WithDirectRedirect
	// (RouterOptions.DirectRedirect); the zero value (false) only matters for callers that build a
	// Handler without going through NewRouter, e.g. tests that don't exercise this route.
	directRedirect bool
}

func newLinkHandler(c *common, manager LinkManager, tmpl, editor, view *template.Template) *LinkHandler {
	return &LinkHandler{
		common:  c,
		manager: manager,
		tmpl:    tmpl,
		editor:  editor,
		view:    view,
		// directRedirect zero = false = interstitial mode; WithDirectRedirect overrides at router build.
		directRedirect: false,
	}
}

// RegisterRoutes registers the create page, the old editor URL and the short-link redirect
// route. The link page GET /links/{id} shares its path with the JSON API, so registerAllRoutes
// registers it (see htmlOr); the editor is at GET /edit/{id} (Handler.EditPage).
func (h *LinkHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /r/{id}", h.Redirect)
	mux.HandleFunc("GET /links", h.CreateLinkPage)
	mux.HandleFunc("GET /links/{id}/edit", redirectToEditor)
}

type redirectViewData struct {
	TargetURL string
	Nonce     string
	Header    headerView
}

// Redirect handles GET /r/{id}, the older short-link URL (Handler.GetNoteOrLink serves GET /{id}
// through respondLink): follows the link with links.Manager.Follow, which applies the privacy,
// expiry and burn rules, then either issues a 301 to the target URL or renders the
// transparency interstitial. The mode is link.EffectiveDirectRedirect(h.directRedirect): anonymous
// links get the interstitial unless the operator enabled --direct-redirect; links created by an
// authenticated owner redirect directly; a per-link override applies within those limits.
func (h *LinkHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	link, err := h.followLink(r, id)
	h.respondLink(w, r, id, link, err)
}

// respondLink writes the answer for a followed link: the privacy denial, the burn-intent refusal
// or another error page, else the 301 or the transparency interstitial.
func (h *LinkHandler) respondLink(w http.ResponseWriter, r *http.Request, id string, link *domain.Link, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		h.denyPrivate(w, r)

		return
	}

	if errors.Is(err, domain.ErrBurnIntentRequired) {
		writeBurnReadNotExplicit(w, r, h.log)

		return
	}

	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	if link.EffectiveDirectRedirect(h.directRedirect) {
		// Not an open redirect (gosec G710 territory), but the feature —
		// links.Manager.Create already restricted link.TargetURL to http/https via
		// validateTargetURL (domain.ErrInvalidURL rejects javascript:/data:/file:/etc at creation
		// time), and redirecting to a URL its own creator chose is a short-link service's entire
		// purpose, the same category as any commercial URL shortener.
		http.Redirect(w, r, link.TargetURL, http.StatusMovedPermanently) //nolint:gosec // G710: creator-chosen target

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := redirectViewData{
		Header:    h.header(r, headerKindPlain),
		TargetURL: link.TargetURL,
		Nonce:     middleware.NonceFromContext(r.Context()),
	}

	execErr := h.tmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render redirect template", "id", domain.HashSlug(id), "err", execErr)
	}
}

// followLink follows the link only as far as the request's intent allows: a deliberate access
// (explicitRead — a browser navigation or a client naming the format) may consume a
// burn_after_click link; anything else (preview fetch, scanner, HEAD, <img src="/…">) may not.
func (h *LinkHandler) followLink(r *http.Request, id string) (*domain.Link, error) {
	reader := h.reader(r)
	reader.AllowBurn = explicitRead(r)

	link, err := h.manager.Follow(r.Context(), id, reader)
	if err != nil {
		return nil, fmt.Errorf("follow link: %w", err)
	}

	return link, nil
}

// WithDirectRedirect sets the short-link redirect's response mode (see RouterOptions.DirectRedirect). Called
// by NewRouter; do not call after the server starts.
func (h *Handler) WithDirectRedirect(v bool) *Handler {
	h.directRedirect = v

	return h
}
