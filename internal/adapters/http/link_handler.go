package http

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"

	_ "embed"
)

//go:embed templates/redirect.html
var redirectTmplSrc string

// LinkHandler serves the browser-facing short-link redirect route (GET /r/{id}). The JSON API
// operations (POST /links, GET/DELETE /links/{id}) are served by OgenHandler instead — see
// ogen_link.go — mirroring how NoteHandler and OgenHandler split notes' browser and API surfaces.
type LinkHandler struct {
	*common

	manager LinkManager
	tmpl    *template.Template

	// directRedirect selects GET /r/{id}'s response mode: true issues an immediate 301 to the
	// target URL, false renders the interstitial (redirect.html). Set via Handler.WithDirectRedirect
	// (RouterOptions.DirectRedirect); the zero value (false) only matters for callers that build a
	// Handler without going through NewRouter, e.g. tests that don't exercise this route.
	directRedirect bool
}

func newLinkHandler(c *common, manager LinkManager, tmpl *template.Template) *LinkHandler {
	return &LinkHandler{
		common:  c,
		manager: manager,
		tmpl:    tmpl,
		// directRedirect zero = false = interstitial mode; WithDirectRedirect overrides at router build.
		directRedirect: false,
	}
}

// RegisterRoutes registers the short-link redirect route.
func (h *LinkHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /r/{id}", h.Redirect)
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

	handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	link, err := h.followLink(r, id)
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

	data := redirectViewData{TargetURL: link.TargetURL, Nonce: nonceFromContext(r.Context())}

	execErr := h.tmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render redirect template", "id", domain.HashSlug(id), "err", execErr)
	}
}

// followLink follows the link only as far as the request's intent allows: a deliberate access
// (explicitRead — a browser navigation or a client naming the format) may consume a
// burn_after_click link; anything else (preview fetch, scanner, HEAD, <img src="/r/…">) may not.
func (h *LinkHandler) followLink(r *http.Request, id string) (*domain.Link, error) {
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

// handlePrivateAuth checks the link's privacy level against the caller (see Link.VisibleTo),
// mirroring NoteHandler.handlePrivateAuth exactly — both now hand a denial to common.denyPrivate,
// so "exactly" is enforced by the shared code rather than by two copies staying in step. GET
// /r/{id} is browser-only (no JSON variant registered for it — see openapi.yaml's "Short links"
// section), but a non-HTML request is still answered sanely (401 JSON) rather than assumed to be
// a browser.
//
// Uses Peek (non-mutating) so a denied caller never triggers Redirect's side effects (view count,
// burn-after-click consumption) — the same reason NoteHandler.Peek exists.
func (h *LinkHandler) handlePrivateAuth(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.authMgr == nil {
		return false
	}

	link, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return true
	}

	if link.VisibleTo(callerIDFromCtx(r.Context()), h.isAuthenticated(r)) {
		return false
	}

	h.denyPrivate(w, r)

	return true
}

// WithDirectRedirect sets GET /r/{id}'s response mode (see RouterOptions.DirectRedirect). Called
// by NewRouter; do not call after the server starts.
func (h *Handler) WithDirectRedirect(v bool) *Handler {
	h.directRedirect = v

	return h
}
