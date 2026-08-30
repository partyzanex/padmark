package http

import (
	"html/template"
	"net/http"
	"net/url"

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
// transparency interstitial. The mode is link.EffectiveDirectRedirect(h.directRedirect): the
// link's own per-link override if it set one, otherwise the server-wide --direct-redirect default.
func (h *LinkHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	link, err := h.manager.Redirect(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	if link.EffectiveDirectRedirect(h.directRedirect) {
		// G710: taint analysis flags this as an open redirect, but it's the feature, not a bug —
		// links.Manager.Create already restricted link.TargetURL to http/https via
		// validateTargetURL (domain.ErrInvalidURL rejects javascript:/data:/file:/etc at creation
		// time), and redirecting to a URL its own creator chose is a short-link service's entire
		// purpose, the same category as any commercial URL shortener.
		http.Redirect(w, r, link.TargetURL, http.StatusMovedPermanently) //nolint:gosec

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := redirectViewData{TargetURL: link.TargetURL, Nonce: nonceFromContext(r.Context())}

	execErr := h.tmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render redirect template", "id", id, "err", execErr)
	}
}

// handlePrivateAuth checks the link's privacy level against the caller (see Link.VisibleTo),
// mirroring NoteHandler.handlePrivateAuth exactly. GET /r/{id} is browser-only (no JSON variant
// registered for it — see openapi.yaml's "Short links" section), but a non-HTML request is still
// answered sanely (401 JSON) rather than assumed to be a browser.
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

	if negotiate(r) == formatHTML {
		loginURL := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, loginURL, http.StatusSeeOther)

		return true
	}

	writeUnauthorized(w)

	return true
}

// WithDirectRedirect sets GET /r/{id}'s response mode (see RouterOptions.DirectRedirect). Called
// by NewRouter; do not call after the server starts.
func (h *Handler) WithDirectRedirect(v bool) *Handler {
	h.directRedirect = v

	return h
}
