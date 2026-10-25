//go:generate go run go.uber.org/mock/mockgen@latest -source=handler.go -destination=handler_mocks_test.go -package=http_test

package http

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// SessionManager authenticates a user and manages their login session lifecycle.
type SessionManager interface {
	Login(
		ctx context.Context, username, password, totpCode, userAgent, clientIP, previousSessionID string,
	) (string, error)
	Logout(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, sessionID string) (*domain.User, error)
	ChangePassword(ctx context.Context, sessionID, oldPassword, newPassword, totpCode string) (string, error)
}

// OnboardingManager creates the first admin account and enrolls new users via invite tokens.
type OnboardingManager interface {
	IsEmpty(ctx context.Context) (bool, error)
	// GenerateInvite issues an invite; issuer carries the bearer key and step-up, if any.
	GenerateInvite(ctx context.Context, adminUserID uuid.UUID, issuer auth.Issuer) (string, error)
	AcceptInvite(ctx context.Context, token, username, password string) (*auth.Enrollment, error)
	AcceptFirstAdmin(ctx context.Context, username, password string) (*auth.Enrollment, error)
}

// UserAdminManager lists and revokes user accounts for the admin panel.
type UserAdminManager interface {
	ListUsers(ctx context.Context, adminUserID uuid.UUID) ([]*domain.User, error)
	RevokeUser(ctx context.Context, adminUserID, targetUserID uuid.UUID) error
}

// APITokenManager issues, resolves, lists, and revokes long-lived API tokens.
type APITokenManager interface {
	// ResolveAPIToken maps a bearer API key to its owning user and the key's public ID (used by
	// the auth middleware), recording last-used. Returns domain.ErrNotFound when the key is
	// unknown, revoked, or expired.
	ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, string, error)
	// CreateAPIToken issues an API key for userID expiring after ttlDays (0 = never), returning
	// the plain key exactly once; a bearer issuer must carry a step-up and becomes the key's issuer.
	CreateAPIToken(ctx context.Context, userID uuid.UUID, issuer auth.Issuer, ttlDays int) (string, error)
	// ListAPITokens returns all API tokens with owning usernames for the admin panel.
	ListAPITokens(ctx context.Context, adminUserID uuid.UUID) ([]*auth.APITokenInfo, error)
	// RevokeAPIToken deletes an API token by its public ID (the token hash).
	RevokeAPIToken(ctx context.Context, adminUserID uuid.UUID, tokenID string) error
}

// AuthManager performs TOTP-based authentication, user management, and API-token issuance.
//
// It is the single seam from the HTTP layer onto the auth Manager — session, onboarding,
// user-admin and API-token operations the handlers consume through one field. It is composed
// of four narrower interfaces (SessionManager, OnboardingManager, UserAdminManager,
// APITokenManager) so each concern is independently readable and mockable per ISP.
type AuthManager interface {
	SessionManager
	OnboardingManager
	UserAdminManager
	APITokenManager
}

//go:embed static
var staticFS embed.FS

//go:embed templates/header.html
var headerTmplSrc string

// StaticHandler serves embedded static assets under /static/.
//
//nolint:gochecknoglobals // package-level FS handler is intentional
var StaticHandler = http.FileServer(http.FS(staticFS))

//nolint:gochecknoglobals // computed once from embedded static content
var styleVersion = staticAssetVersion("static/style.css")

func staticAssetVersion(path string) string {
	data, err := staticFS.ReadFile(path)
	if err != nil {
		return "dev"
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:12]
}

// RevealTokenStore issues and consumes one-time reveal tokens for burn-after-reading notes.
type RevealTokenStore interface {
	Issue(ctx context.Context, noteID string) (string, error)
	// Consume atomically marks tok as used only when it is bound to noteID,
	// is unused, and has not expired. Returns false if any condition is unmet,
	// leaving the token intact so the legitimate owner can still use it.
	Consume(ctx context.Context, tok, noteID string) bool
}

// NoteManager is the interface the HTTP adapter requires from the business logic layer.
type NoteManager interface {
	Create(ctx context.Context, note *domain.Note) (*domain.Note, error)
	Peek(ctx context.Context, id string) (*domain.Note, error)
	View(ctx context.Context, id string) (*domain.Note, error)
	ViewPreloaded(ctx context.Context, id string, preloaded *domain.Note) (*domain.Note, error)
	GetRendered(ctx context.Context, id string) (*domain.Note, string, error)
	GetRenderedPreloaded(ctx context.Context, id string, preloaded *domain.Note) (*domain.Note, string, error)
	// EnforceExpiry deletes an expired, already-fetched note and returns ErrExpired, without
	// consuming a burn note or starting its timer. See notes.Manager.EnforceExpiry.
	EnforceExpiry(ctx context.Context, id string, note *domain.Note) error
	// Update and Delete accept callerID — the authenticated caller's user ID ("" if anonymous) —
	// so the note's owner can bypass editCode. See notes.Manager.Update/Delete.
	Update(ctx context.Context, id, editCode string, callerID uuid.UUID, note *domain.Note) (*domain.Note, error)
	Delete(ctx context.Context, id, editCode string, callerID uuid.UUID) error
}

// LinkManager is the interface the HTTP adapter requires from the short-link business logic
// layer — the links analogue of NoteManager. Shared by OgenHandler (JSON API: POST /links,
// GET/PUT/DELETE /links/{id}) and LinkHandler (browser-facing GET /r/{id}).
type LinkManager interface {
	Create(ctx context.Context, targetURL string, opts links.CreateOptions) (*domain.Link, error)
	Update(
		ctx context.Context, slug, editCode string, callerID uuid.UUID, targetURL string, opts links.UpdateOptions,
	) (*domain.Link, error)
	Peek(ctx context.Context, slug string) (*domain.Link, error)
	Follow(ctx context.Context, slug string, options links.FollowOptions) (*domain.Link, error)
	Redirect(ctx context.Context, slug string) (*domain.Link, error)
	// RedirectWithoutBurn follows a link like Redirect but never consumes a burn_after_click
	// link (domain.ErrBurnIntentRequired). See links.Manager.RedirectWithoutBurn.
	RedirectWithoutBurn(ctx context.Context, slug string) (*domain.Link, error)
	Delete(ctx context.Context, slug, editCode string, callerID uuid.UUID) error
}

// Pinger checks database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// NoPinger is a no-op Pinger for use when database health checks are not required.
type NoPinger struct{}

func (NoPinger) PingContext(_ context.Context) error { return nil }

// common holds the collaborators and config shared by every concern-specific handler
// (NoteHandler, AuthHandler, AdminHandler, PageHandler): logging, error-page rendering,
// auth-manager access, and the request-scheme/proxy/CSRF config set once at startup via the
// Handler facade's With* methods. A single *common instance is embedded (by pointer) in the
// facade and in each concern handler, so a With* call is visible everywhere immediately.
type common struct {
	log            *slog.Logger
	errorTmpl      *template.Template
	authMgr        AuthManager
	forcedScheme   string
	csrfSecret     []byte
	trustedProxies []*net.IPNet
	// bootstrapDigest is the SHA-256 of the configured bootstrap token; nil keeps first-admin
	// setup closed (see RouterOptions.BootstrapToken and bootstrapTokenValid).
	bootstrapDigest []byte
	sessionTTL      time.Duration
}

// sessionMaxAge returns the TOTP session cookie's Max-Age: the configured SessionTTL (see
// RouterOptions.SessionTTL), or auth.DefaultSessionTTL when unset — the same fallback
// auth.NewSessionManager applies to the actual server-side session lifetime, so an unconfigured
// deployment still gets a cookie that matches how long its sessions really live.
func (c *common) sessionMaxAge() time.Duration {
	if c.sessionTTL > 0 {
		return c.sessionTTL
	}

	return auth.DefaultSessionTTL
}

// guard wraps next with CSRF verification using the secret set via Handler.WithCSRFSecret.
func (c *common) guard(next http.HandlerFunc) http.HandlerFunc {
	return csrfGuard(c.csrfSecret, next)
}

// scheme returns the scheme ("http" or "https") to use when building absolute links back to
// this server: the operator-forced override when set (--public-scheme), otherwise the
// auto-detected value from TLS/X-Forwarded-Proto (see requestScheme).
func (c *common) scheme(r *http.Request) string {
	return requestScheme(r, c.forcedScheme, c.trustedProxies)
}

// isHTTPS reports whether cookies issued for this request must carry the Secure attribute:
// the operator-forced "https" scheme counts, as do direct TLS and a trusted proxy's
// X-Forwarded-Proto (see the package-level isHTTPS for the precedence rules).
func (c *common) isHTTPS(r *http.Request) bool {
	return isHTTPS(r, c.forcedScheme, c.trustedProxies)
}

// denyPrivate answers a caller that failed a resource's privacy check. Shared by
// NoteHandler.handlePrivateAuth and LinkHandler.Redirect so their denial responses cannot drift.
//
// An anonymous browser is sent to the login page carrying its original target: signing in is
// exactly what would grant access. A browser that is already signed in gets 403 instead — it is
// not missing a credential, it holds the wrong one, and bouncing it to /login is a loop that ends
// where it started.
//
// Non-browser callers keep the machine-readable 401 either way. Splitting that into 403 as well
// would be an API contract change (401 is what openapi.yaml documents on these paths), not a
// navigation fix.
func (c *common) denyPrivate(w http.ResponseWriter, r *http.Request) {
	if negotiate(r) != formatHTML {
		writeUnauthorized(w)

		return
	}

	if c.isAuthenticated(r) {
		data := privateToOtherPageData()
		c.writeErrorPageData(w, r, &data)

		return
	}

	loginURL := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
}

// isAuthenticated reports whether the request carries a valid auth credential.
// Accepts a TOTP session cookie, or no auth when the account system is unconfigured.
func (c *common) isAuthenticated(r *http.Request) bool {
	if c.authMgr == nil {
		return true
	}

	// The auth middleware already resolved the session and stored the user in context.
	// Reading from context avoids a redundant DB round-trip for auth-protected routes.
	if userFromContext(r) != nil {
		return true
	}

	// For public routes the middleware does not run, so fall back to a direct session check.
	if sessID := extractSessionID(r); sessID != "" {
		_, err := c.authMgr.GetSession(r.Context(), sessID)
		if err == nil {
			return true
		}
	}

	return false
}

// signedIn reports whether the request carries a valid credential of the account system. Unlike
// isAuthenticated it is false when accounts are off: then nobody can sign in.
func (c *common) signedIn(r *http.Request) bool {
	return c.authMgr != nil && c.isAuthenticated(r)
}

// parseTmpl parses a page template together with the shared header partial.
func parseTmpl(name, src string) *template.Template {
	return template.Must(
		template.Must(template.New(name).Funcs(template.FuncMap{
			"styleVersion": func() string { return styleVersion },
		}).Parse(src)).Parse(headerTmplSrc),
	)
}

// Handler composes the five concern-specific handlers — NoteHandler (web_note.go, web_edit.go),
// LinkHandler (link_handler.go), AuthHandler (web_auth.go), AdminHandler (web_admin.go), and
// PageHandler (web_index.go, web_success.go, web_apidocs.go) — into the single value routing and
// the composition root consume. Each concern owns only the collaborators and templates it needs;
// common (this file) holds what is genuinely cross-cutting. Go embedding promotes each concern's
// methods, so Handler needs no methods of its own beyond construction and the With* wiring below.
type Handler struct {
	*common
	*NoteHandler
	*LinkHandler
	*AuthHandler
	*AdminHandler
	*PageHandler
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager NoteManager, linkManager LinkManager, log *slog.Logger) *Handler {
	noteTmpl := parseTmpl("note", noteTmplSrc)
	indexTmpl := parseTmpl("index", indexTmplSrc)
	loginTmpl := parseTmpl("login", loginTmplSrc)
	setupTmpl := parseTmpl("setup", setupTmplSrc)
	adminTmpl := parseTmpl("admin", adminTmplSrc)
	changePwTmpl := parseTmpl("change_password", changePwTmplSrc)
	apidocsTmpl := parseTmpl("apidocs", apidocsTmplSrc)
	successTmpl := parseTmpl("success", successTmplSrc)
	errorTmpl := parseTmpl("error", errorTmplSrc)
	redirectTmpl := parseTmpl("redirect", redirectTmplSrc)

	cmn := &common{
		log:       log,
		errorTmpl: errorTmpl,
		// authMgr, forcedScheme, csrfSecret, trustedProxies, sessionTTL are zero-valued here
		// and attached later via WithAuthManager / WithForcedScheme / WithCSRFSecret /
		// WithTrustedProxies / WithSessionTTL from the composition root. NewRouter calls all
		// of them before the server starts serving requests — missing one is a startup bug,
		// caught on guarded POSTs (csrfSecret=nil → csrfGuard rejects with 403).
		authMgr:        nil,
		forcedScheme:   "",
		csrfSecret:     nil,
		trustedProxies: nil,
		sessionTTL:     0,
		// Set by WithBootstrapToken; nil keeps first-admin setup closed.
		bootstrapDigest: nil,
	}

	return &Handler{
		common:       cmn,
		NoteHandler:  newNoteHandler(cmn, manager, noteTmpl, indexTmpl),
		LinkHandler:  newLinkHandler(cmn, linkManager, redirectTmpl),
		AuthHandler:  newAuthHandler(cmn, loginTmpl, setupTmpl, changePwTmpl),
		AdminHandler: newAdminHandler(cmn, adminTmpl),
		PageHandler:  newPageHandler(cmn, indexTmpl, successTmpl, apidocsTmpl),
	}
}

// WithRevealStore attaches a RevealTokenStore so that burn-after-reading notes
// show a confirmation interstitial before being consumed.
// When not set, burn notes are served immediately (backward-compatible).
func (h *Handler) WithRevealStore(store RevealTokenStore) *Handler {
	h.revealStore = store

	return h
}

// WithAuthManager attaches a TOTP AuthManager enabling session-based authentication.
func (h *Handler) WithAuthManager(mgr AuthManager) *Handler {
	h.authMgr = mgr

	return h
}

// WithBootstrapToken sets the token that authorises creating the first admin via /setup without an
// invite (RouterOptions.BootstrapToken). Only its digest is kept; empty leaves that setup closed.
// Called by NewRouter before any request is served; do not call after the server starts.
func (h *Handler) WithBootstrapToken(token string) *Handler {
	h.bootstrapDigest = nil

	if token != "" {
		sum := sha256.Sum256([]byte(token))
		h.bootstrapDigest = sum[:]
	}

	return h
}

// WithCSRFSecret sets the HMAC key used to sign and verify CSRF tokens.
// Called by NewRouter before any request is served; do not call after the server starts.
func (h *Handler) WithCSRFSecret(secret []byte) *Handler {
	h.csrfSecret = secret

	return h
}

// WithTrustedProxies sets the CIDR blocks whose X-Forwarded-Proto header is trusted.
// Called by NewRouter; do not call after the server starts.
func (h *Handler) WithTrustedProxies(proxies []*net.IPNet) *Handler {
	h.trustedProxies = proxies

	return h
}

// WithForcedScheme overrides the scheme ("http" or "https") used when building absolute links
// back to this server, bypassing TLS/X-Forwarded-Proto auto-detection entirely. "https" also
// marks every request as HTTPS for the session/CSRF cookie Secure attribute and HSTS (see
// isHTTPS), since behind a TLS-terminating proxy that is the only signal the server has. Pass an
// empty string to keep auto-detection. Called by NewRouter; do not call after the server starts.
func (h *Handler) WithForcedScheme(scheme string) *Handler {
	h.forcedScheme = scheme

	return h
}

// WithSessionTTL sets the Max-Age used by the TOTP session cookie (see RouterOptions.SessionTTL
// for why this must be kept in sync with auth.NewManager's sessionTTL). Zero or negative falls
// back to auth.DefaultSessionTTL at read time (see common.sessionMaxAge), matching
// auth.NewSessionManager's own fallback. Called by NewRouter; do not call after the server starts.
func (h *Handler) WithSessionTTL(ttl time.Duration) *Handler {
	h.sessionTTL = ttl

	return h
}
