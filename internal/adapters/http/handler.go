//go:generate go run go.uber.org/mock/mockgen@latest -source=handler.go -destination=handler_mocks_test.go -package=http_test

package http

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"log/slog"
	"maps"
	"net"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// AuthManager performs TOTP-based authentication, user management, and API-token issuance.
//
// It is the single seam from the HTTP layer onto the auth Manager — session, onboarding,
// user-admin and API-token operations the handlers consume through one field. Splitting it
// would not lower the linter's count (embedded methods still count) and would fragment a
// cohesive, single-implementation dependency, so interfacebloat is suppressed here.
//
//nolint:interfacebloat // cohesive single-implementation seam; rationale in the doc comment above.
type AuthManager interface {
	Login(ctx context.Context, username, password, totpCode, userAgent, clientIP string) (string, error)
	Logout(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, sessionID string) (*domain.User, error)
	GenerateInvite(ctx context.Context, adminUserID string) (string, error)
	AcceptInvite(ctx context.Context, token, username, password string) (string, error)
	AcceptFirstAdmin(ctx context.Context, username, password string) (string, error)
	ChangePassword(ctx context.Context, sessionID, oldPassword, newPassword, totpCode string) (string, error)
	IsEmpty(ctx context.Context) (bool, error)
	ListUsers(ctx context.Context, adminUserID string) ([]*domain.User, error)
	RevokeUser(ctx context.Context, adminUserID, targetUserID string) error
	// ResolveAPIToken maps a bearer API key to its owning user (used by the auth middleware),
	// recording last-used. Returns domain.ErrNotFound when the key is unknown, revoked, or expired.
	ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, error)
	// CreateAPIToken issues a long-lived API key for userID, returning the plain key exactly once.
	CreateAPIToken(ctx context.Context, userID string) (string, error)
	// ListAPITokens returns all API tokens with owning usernames for the admin panel.
	ListAPITokens(ctx context.Context, adminUserID string) ([]*auth.APITokenInfo, error)
	// RevokeAPIToken deletes an API token by its public ID (the token hash).
	RevokeAPIToken(ctx context.Context, adminUserID, tokenID string) error
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
	Update(ctx context.Context, id, editCode string, note *domain.Note) (*domain.Note, error)
	Delete(ctx context.Context, id, editCode string) error
}

// Pinger checks database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// NoPinger is a no-op Pinger for use when database health checks are not required.
type NoPinger struct{}

func (NoPinger) PingContext(_ context.Context) error { return nil }

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	manager        NoteManager
	authMgr        AuthManager
	revealStore    RevealTokenStore
	log            *slog.Logger
	noteTmpl       *template.Template
	indexTmpl      *template.Template
	loginTmpl      *template.Template
	setupTmpl      *template.Template
	adminTmpl      *template.Template
	changePwTmpl   *template.Template
	apidocsTmpl    *template.Template
	successTmpl    *template.Template
	errorTmpl      *template.Template
	allowedTokens  map[string]struct{} // legacy bearer-token auth; deprecated, see NewHandler
	csrfSecret     []byte
	trustedProxies []*net.IPNet
}

// parseTmpl parses a page template together with the shared header partial.
func parseTmpl(name, src string) *template.Template {
	return template.Must(
		template.Must(template.New(name).Funcs(template.FuncMap{
			"styleVersion": func() string { return styleVersion },
		}).Parse(src)).Parse(headerTmplSrc),
	)
}

// NewHandler creates a Handler with required dependencies.
//
// tokens is the list of valid bearer auth tokens; pass nil to disable token auth.
// NOTE: the tokens parameter drives the DEPRECATED legacy bearer-token write auth
// (PADMARK_AUTH_TOKENS), superseded by the TOTP account system (WithAuthManager /
// --enable-accounts) and slated for removal. New callers should pass nil and use accounts.
func NewHandler(manager NoteManager, log *slog.Logger, tokens []string) *Handler {
	handler := &Handler{
		manager:      manager,
		log:          log,
		noteTmpl:     parseTmpl("note", noteTmplSrc),
		indexTmpl:    parseTmpl("index", indexTmplSrc),
		loginTmpl:    parseTmpl("login", loginTmplSrc),
		setupTmpl:    parseTmpl("setup", setupTmplSrc),
		adminTmpl:    parseTmpl("admin", adminTmplSrc),
		changePwTmpl: parseTmpl("change_password", changePwTmplSrc),
		apidocsTmpl:  parseTmpl("apidocs", apidocsTmplSrc),
		successTmpl:  parseTmpl("success", successTmplSrc),
		errorTmpl:    parseTmpl("error", errorTmplSrc),
	}

	if len(tokens) > 0 {
		handler.allowedTokens = makeTokenSet(tokens)
	}

	return handler
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

// AllowedTokens returns a defensive copy of the bearer-token set.
// The copy prevents callers from mutating the handler's internal state.
//
// Deprecated: part of the legacy bearer-token write auth (PADMARK_AUTH_TOKENS), superseded by
// the TOTP account system (--enable-accounts). Will be removed in a future release.
func (h *Handler) AllowedTokens() map[string]struct{} {
	return maps.Clone(h.allowedTokens)
}

// isAuthenticated reports whether the request carries a valid auth credential.
// Accepts a TOTP session cookie, a bearer token, or no auth when both are unconfigured.
func (h *Handler) isAuthenticated(r *http.Request) bool {
	if h.allowedTokens == nil && h.authMgr == nil {
		return true
	}

	// The auth middleware already resolved the session and stored the user in context.
	// Reading from context avoids a redundant DB round-trip for auth-protected routes.
	if userFromContext(r) != nil {
		return true
	}

	// For public routes the middleware does not run, so fall back to a direct session check.
	if h.authMgr != nil {
		if sessID := extractSessionID(r); sessID != "" {
			_, err := h.authMgr.GetSession(r.Context(), sessID)
			if err == nil {
				return true
			}
		}
	}

	if h.allowedTokens != nil {
		token := extractToken(r)
		_, ok := h.allowedTokens[token]

		return ok
	}

	return false
}
