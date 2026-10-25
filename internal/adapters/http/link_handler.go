package http

import (
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"

	"github.com/partyzanex/padmark/internal/adapters/http/middleware"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

//go:embed templates/redirect.html
var redirectTmplSrc string

// LinkHandler serves the browser's link editor and redirect routes. OgenHandler serves
// the JSON API, mirroring the split between note pages and note API operations.
type LinkHandler struct {
	*common

	manager LinkManager
	tmpl    *template.Template
	editor  *template.Template

	// directRedirect selects GET /r/{id}'s response mode: true issues an immediate 301 to the
	// target URL, false renders the interstitial (redirect.html). Set via Handler.WithDirectRedirect
	// (RouterOptions.DirectRedirect); the zero value (false) only matters for callers that build a
	// Handler without going through NewRouter, e.g. tests that don't exercise this route.
	directRedirect bool
}

func newLinkHandler(c *common, manager LinkManager, tmpl, editor *template.Template) *LinkHandler {
	return &LinkHandler{
		common:  c,
		manager: manager,
		tmpl:    tmpl,
		editor:  editor,
		// directRedirect zero = false = interstitial mode; WithDirectRedirect overrides at router build.
		directRedirect: false,
	}
}

// RegisterRoutes registers creation/editing pages and the short-link redirect route.
func (h *LinkHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /r/{id}", h.Redirect)
	mux.HandleFunc("GET /links", h.CreateLinkPage)
	mux.HandleFunc("GET /links/{id}/edit", h.EditLinkPage)
}

type redirectViewData struct {
	TargetURL string
	Nonce     string
}

// Redirect handles GET /r/{id}: resolves the link, applies its privacy gate (mirrors
// NoteHandler.handlePrivateAuth), then either issues a 301 to the target URL or renders the
// transparency interstitial. The mode is link.EffectiveDirectRedirect(h.directRedirect): anonymous
// links get the interstitial unless the operator enabled --direct-redirect; links created by an
// authenticated owner redirect directly; a per-link override applies within those limits.
func (h *LinkHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	link, err := h.followLink(r, id)
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
		http.Redirect(w, r, link.TargetURL, http.StatusMovedPermanently)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := redirectViewData{TargetURL: link.TargetURL, Nonce: middleware.NonceFromContext(r.Context())}

	execErr := h.tmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render redirect template", "id", domain.HashSlug(id), "err", execErr)
	}
}

// followLink follows the link only as far as the request's intent allows: a deliberate access
// (explicitRead — a browser navigation or a client naming the format) may consume a
// burn_after_click link; anything else (preview fetch, scanner, HEAD, <img src="/r/…">) may not.
func (h *LinkHandler) followLink(r *http.Request, id string) (*domain.Link, error) {
	if h.authMgr != nil {
		link, err := h.manager.Follow(r.Context(), id, links.FollowOptions{
			CallerID: middleware.CallerIDFromCtx(r.Context()), Authenticated: h.isAuthenticated(r), AllowBurn: explicitRead(r),
		})
		if err != nil {
			return nil, fmt.Errorf("follow link: %w", err)
		}

		return link, nil
	}

	if explicitRead(r) {
		link, err := h.manager.Redirect(r.Context(), id)
		if err != nil {
			return nil, fmt.Errorf("follow link: %w", err)
		}

		return link, nil
	}

	link, err := h.manager.RedirectWithoutBurn(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("follow link: %w", err)
	}

	return link, nil
}

// WithDirectRedirect sets GET /r/{id}'s response mode (see RouterOptions.DirectRedirect). Called
// by NewRouter; do not call after the server starts.
func (h *Handler) WithDirectRedirect(v bool) *Handler {
	h.directRedirect = v

	return h
}
