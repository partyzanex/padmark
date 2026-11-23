package web

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// ── setup (GET+POST /setup) ──.

//go:embed templates/setup.html
var setupTmplSrc string

type setupViewData struct {
	Nonce        string
	Token        string
	Error        string
	CSRFToken    string
	QRCode       template.URL
	Header       headerView
	IsFirstAdmin bool
}

// SetupPage handles GET /setup — renders the account creation form.
func (h *AuthHandler) SetupPage(w http.ResponseWriter, r *http.Request) {
	if h.accounts == accountsOff {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	token := r.URL.Query().Get("invite")

	// Without an invite, /setup only works while bootstrapping the first admin.
	// Once an admin exists it is closed (403) — extra accounts come via invite links.
	if token == "" && !h.bootstrapOpen(w, r) {
		return
	}

	if token == "" && !h.bootstrapEnabled(w, r) {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.setupTmpl.Execute(w, setupViewData{
		Header:       h.header(r, headerKindPlain),
		Nonce:        middleware.NonceFromContext(r.Context()),
		Token:        token,
		IsFirstAdmin: token == "",
		CSRFToken:    middleware.CSRFFromContext(r.Context()),
		// Error/QRCode unset on initial GET render; populated on POST failure or QR display.
		Error:  "",
		QRCode: "",
	})
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", renderErr)
	}
}

func setupErrMsg(err error) string {
	switch {
	case errors.Is(err, domain.ErrInviteExpired):
		return "Invite link has expired."
	case errors.Is(err, domain.ErrInviteUsed):
		return "Invite link has already been used."
	case errors.Is(err, domain.ErrUserExists):
		return "Username is already taken."
	case errors.Is(err, domain.ErrForbidden):
		return "Setup is only available when no users exist."
	case errors.Is(err, domain.ErrWeakPassword):
		return "Password must be at least 12 characters with upper, lower, digit, and special character."
	case errors.Is(err, domain.ErrInvalidUsername):
		return domain.ErrInvalidUsername.Error()
	default:
		return "Something went wrong. Please try again."
	}
}

// SetupHandler handles POST /setup — creates user and returns QR code (HTML) or the otpauth
// enrollment URI (JSON, headless provisioning).
func (h *AuthHandler) SetupHandler(w http.ResponseWriter, r *http.Request) {
	if h.accounts == accountsOff {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	if negotiate(r) == formatJSON {
		h.setupJSON(w, r)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	username := r.FormValue("username")
	password := r.FormValue("password")
	passwordConfirm := r.FormValue("password_confirm")
	token := r.FormValue("invite")

	data := setupViewData{
		Header:       h.header(r, headerKindPlain),
		Nonce:        middleware.NonceFromContext(r.Context()),
		CSRFToken:    middleware.CSRFFromContext(r.Context()),
		Token:        token,
		IsFirstAdmin: token == "",
		// Error/QRCode populated below in the failure/success branches; zero on initial render.
		Error:  "",
		QRCode: "",
	}

	if password != passwordConfirm {
		data.Error = "Passwords do not match."
		h.renderSetup(w, r, &data)

		return
	}

	// Without an invite, creating an account is only allowed while bootstrapping the
	// first admin; once one exists the endpoint is closed (403). Bootstrapping itself demands
	// the operator's bootstrap token, so the first visitor cannot claim the server.
	if token == "" && (!h.bootstrapOpen(w, r) || !h.bootstrapProven(w, r, &data)) {
		return
	}

	var (
		enrollment *auth.Enrollment
		setupErr   error
	)

	if token != "" {
		enrollment, setupErr = h.onboarding.AcceptInvite(r.Context(), token, username, password)
	} else {
		enrollment, setupErr = h.onboarding.AcceptFirstAdmin(r.Context(), username, password)
	}

	if setupErr != nil {
		data.Error = setupErrMsg(setupErr)
	} else {
		data.QRCode = template.URL(enrollment.QRCode) //nolint:gosec // data: URL from internal crypto, not user input
	}

	h.renderSetup(w, r, &data)
}

// setupGuard enforces CSRF only for the HTML branch of POST /setup. A JSON client carries no
// cookie session, and an invite token — when present — is itself the bearer-equivalent
// credential, so there is no session to forge. The branch is chosen by content negotiation:
// browsers list text/html first and ?raw=1 resolves to formatPlain, so only machine callers
// (Accept: application/json, */*, or no Accept header) reach the unguarded JSON path. A
// scripted caller that previously posted the HTML form with */* never worked anyway — CSRF
// rejected it — so no working client crosses the branch boundary.
func (h *AuthHandler) setupGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if negotiate(r) != formatJSON {
			h.guard(next)(w, r)

			return
		}

		// The body must be application/json (parameters like charset allowed). Safelisted types
		// such as text/plain make a cross-site fetch preflight-free; a non-safelisted content
		// type forces the browser into an OPTIONS preflight it cannot complete without a CORS
		// approval — this holds even where Fetch Metadata is unavailable.
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			response.WriteJSONErrorStatus(w, r, h.log, http.StatusUnsupportedMediaType,
				"setup requires Content-Type: application/json")

			return
		}

		// A page can still force Accept: application/json via a cross-site fetch. fetch-metadata
		// flags that: browsers send Sec-Fetch-Site on every request, plain HTTP clients never do.
		// "none" (direct address-bar) and "same-origin" are the acceptable values — an absent
		// header is covered by the two checks below.
		switch site := r.Header.Get("Sec-Fetch-Site"); site {
		case "", "same-origin", "none":
		default:
			response.WriteJSONErrorStatus(w, r, h.log, http.StatusForbidden, "setup requires a same-origin request")

			return
		}

		// When a browser announced its Origin, it must be this request's own origin: a
		// cross-site POST always carries Origin, so the host comparison settles it even where
		// Sec-Fetch-Site is missing. Machine clients send neither header.
		if origin := r.Header.Get("Origin"); origin != "" && !middleware.SameOrigin(r, origin) {
			response.WriteJSONErrorStatus(w, r, h.log, http.StatusForbidden, "setup requires a same-origin request")

			return
		}

		next(w, r)
	}
}

// isJSONContentType reports whether a Content-Type header value names application/json
// (parameters such as charset are allowed to follow).
func isJSONContentType(header string) bool {
	mime, _, _ := strings.Cut(header, ";")

	return strings.EqualFold(strings.TrimSpace(mime), mimeJSON)
}

// bootstrapOpen reports whether first-admin setup is still allowed (no users yet).
// When closed (an admin already exists) it writes the 403 "setup closed" page and returns
// false; on a lookup error it writes 500 and returns false.
func (h *AuthHandler) bootstrapOpen(w http.ResponseWriter, r *http.Request) bool {
	empty, err := h.onboarding.IsEmpty(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "check empty", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return false
	}

	if !empty {
		data := setupClosedPageData()
		h.writeErrorPageData(w, r, &data)

		return false
	}

	return true
}

// bootstrapEnabled renders the "setup is disabled" page when no bootstrap token is configured.
func (h *AuthHandler) bootstrapEnabled(w http.ResponseWriter, r *http.Request) bool {
	if len(h.bootstrapDigest) > 0 {
		return true
	}

	data := setupDisabledPageData()
	h.writeErrorPageData(w, r, &data)

	return false
}

// bootstrapProven checks the form's bootstrap token for a first-admin setup: with none configured
// the disabled page is shown, a wrong or missing one re-renders the form with 403.
func (h *AuthHandler) bootstrapProven(w http.ResponseWriter, r *http.Request, data *setupViewData) bool {
	if !h.bootstrapEnabled(w, r) {
		return false
	}

	if h.bootstrapTokenValid(r.FormValue("bootstrap_token")) {
		return true
	}

	data.Error = "Invalid bootstrap token."

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	h.renderSetup(w, r, data)

	return false
}

// bootstrapTokenValid reports whether presented equals the configured bootstrap token. Digests
// are compared in constant time, so neither the token's content nor its length leaks through
// timing; with no token configured nothing is valid.
func (c *common) bootstrapTokenValid(presented string) bool {
	if len(c.bootstrapDigest) == 0 || presented == "" {
		return false
	}

	sum := sha256.Sum256([]byte(presented))

	return subtle.ConstantTimeCompare(sum[:], c.bootstrapDigest) == 1
}

// renderSetup renders the setup template, logging any render error.
func (h *AuthHandler) renderSetup(w http.ResponseWriter, r *http.Request, data *setupViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.setupTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", err)
	}
}
