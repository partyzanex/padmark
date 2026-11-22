//go:generate go run go.uber.org/mock/mockgen@latest -source=handler.go -destination=handler_mocks_test.go -package=web_test

package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// SessionManager authenticates a user and manages their login session lifecycle.
type SessionManager interface {
	Login(ctx context.Context, req *auth.LoginRequest) (string, error)
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

// AuthManager is the account system as the composition root gives it to Handler.WithAuthManager.
// No handler keeps it: each handler keeps only the narrow interfaces it calls (ISP) —
// AuthHandler the SessionManager and the OnboardingManager, AdminHandler the UserAdminManager,
// the APITokenManager and the OnboardingManager, common the middleware.SessionChecker. Without
// the account system each field holds disabledAccounts, so no handler needs a nil check.
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

// staticVersionHexLength is the number of hex characters kept from the asset SHA-256 digest.
const staticVersionHexLength = 12

func staticAssetVersion(path string) string {
	data, err := staticFS.ReadFile(path)
	if err != nil {
		return "dev"
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:staticVersionHexLength]
}

// RevealTokenStore issues and consumes one-time reveal tokens for burn-after-reading notes.
type RevealTokenStore interface {
	Issue(ctx context.Context, noteID string) (string, error)
	// Consume atomically marks tok as used only when it is bound to noteID, is unused, and has
	// not expired. It returns false and no error when a condition is not met; the token then
	// stays intact for its owner. It returns an error only when the store fails (fail-hard):
	// the caller then must not reveal the note.
	Consume(ctx context.Context, tok, noteID string) (bool, error)
}

// NoteManager is the interface the HTTP adapter requires from the business logic layer. The
// usecase applies the access rules with the domain.Reader of the request; the handlers only
// translate the errors (see notes.Manager.Read for the order of the rules).
type NoteManager interface {
	Create(ctx context.Context, note *domain.Note) (*domain.Note, error)
	// Inspect applies privacy and expiry without a burn or a view count: the editor and the
	// burn confirmation page use it.
	Inspect(ctx context.Context, id string, reader domain.Reader) (*domain.Note, error)
	Read(ctx context.Context, id string, reader domain.Reader) (*domain.Note, error)
	ReadRendered(ctx context.Context, id string, reader domain.Reader) (*domain.Note, string, error)
	Update(ctx context.Context, id, editCode string, reader domain.Reader, note *domain.Note) (*domain.Note, error)
	Delete(ctx context.Context, id, editCode string, reader domain.Reader) error
}

// LinkManager is the interface the HTTP adapter requires from the short-link business logic
// layer — the links analogue of NoteManager. Shared by OgenHandler (JSON API: POST /links,
// GET/PUT/DELETE /links/{id}) and LinkHandler (browser-facing GET /{id}, GET /r/{id}, the link
// page GET /links/{id} and the editor). The usecase applies the access rules with the
// domain.Reader of the request (see links.Manager.Follow).
type LinkManager interface {
	Create(ctx context.Context, targetURL string, opts links.CreateOptions) (*domain.Link, error)
	Update(
		ctx context.Context, slug, editCode string, reader domain.Reader, targetURL string, opts links.UpdateOptions,
	) (*domain.Link, error)
	// Inspect applies privacy and expiry without a burn or a view count: the link page, the
	// metadata read and the editor use it.
	Inspect(ctx context.Context, slug string, reader domain.Reader) (*domain.Link, error)
	Follow(ctx context.Context, slug string, reader domain.Reader) (*domain.Link, error)
	Delete(ctx context.Context, slug, editCode string, reader domain.Reader) error
}

// disabledAccounts is the account system of a server without accounts (--enable-accounts off).
// Each method returns domain.ErrFeatureNotSupported, so a handler that calls it without a check
// of common.accounts gets the "not enabled" answer and never a nil dereference.
type disabledAccounts struct{}

func (disabledAccounts) Login(context.Context, *auth.LoginRequest) (string, error) {
	return "", domain.ErrFeatureNotSupported
}

func (disabledAccounts) Logout(context.Context, string) error { return domain.ErrFeatureNotSupported }

func (disabledAccounts) GetSession(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledAccounts) ChangePassword(context.Context, string, string, string, string) (string, error) {
	return "", domain.ErrFeatureNotSupported
}

func (disabledAccounts) IsEmpty(context.Context) (bool, error) {
	return false, domain.ErrFeatureNotSupported
}

func (disabledAccounts) GenerateInvite(context.Context, uuid.UUID, auth.Issuer) (string, error) {
	return "", domain.ErrFeatureNotSupported
}

func (disabledAccounts) AcceptInvite(context.Context, string, string, string) (*auth.Enrollment, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledAccounts) AcceptFirstAdmin(context.Context, string, string) (*auth.Enrollment, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledAccounts) ListUsers(context.Context, uuid.UUID) ([]*domain.User, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledAccounts) RevokeUser(context.Context, uuid.UUID, uuid.UUID) error {
	return domain.ErrFeatureNotSupported
}

func (disabledAccounts) ResolveAPIToken(context.Context, string) (*domain.User, string, error) {
	return nil, "", domain.ErrFeatureNotSupported
}

func (disabledAccounts) CreateAPIToken(context.Context, uuid.UUID, auth.Issuer, int) (string, error) {
	return "", domain.ErrFeatureNotSupported
}

func (disabledAccounts) ListAPITokens(context.Context, uuid.UUID) ([]*auth.APITokenInfo, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledAccounts) RevokeAPIToken(context.Context, uuid.UUID, string) error {
	return domain.ErrFeatureNotSupported
}

// Pinger checks database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// NoPinger is a no-op Pinger for use when database health checks are not required.
type NoPinger struct{}

// PingContext always reports a healthy database.
func (NoPinger) PingContext(_ context.Context) error { return nil }

// common holds the collaborators and config shared by every concern-specific handler
// (NoteHandler, AuthHandler, AdminHandler, PageHandler): logging, error-page rendering,
// auth-manager access, and the request-scheme/proxy/CSRF config set once at startup via the
// Handler facade's With* methods. A single *common instance is embedded (by pointer) in the
// facade and in each concern handler, so a With* call is visible everywhere immediately.
type common struct {
	log       *slog.Logger
	errorTmpl *template.Template
	// sessionChecker resolves a session for the page header and the auth middleware.
	sessionChecker middleware.SessionChecker
	// fileMgr is the file usecase, or nil when files are off. The header shows the file tab
	// and the success page reads the file name only when it is set (see WithFileManager).
	fileMgr        FileManager
	forcedScheme   string
	csrfSecret     []byte
	trustedProxies []*net.IPNet
	// bootstrapDigest is the SHA-256 of the configured bootstrap token; nil keeps first-admin
	// setup closed (see RouterOptions.BootstrapToken and bootstrapTokenValid).
	bootstrapDigest []byte
	sessionTTL      time.Duration
	// accounts tells whether the account system is on (WithAuthManager). Without it no privacy
	// level applies: reader sets domain.Reader.SkipPrivacy.
	accounts accountMode
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
	return middleware.CSRFGuard(c.csrfSecret, next)
}

// scheme returns the scheme ("http" or "https") to use when building absolute links back to
// this server: the operator-forced override when set (--public-scheme), otherwise the
// auto-detected value from TLS/X-Forwarded-Proto (see RequestScheme).
func (c *common) scheme(r *http.Request) string {
	return middleware.RequestScheme(r, c.forcedScheme, c.trustedProxies)
}

// isHTTPS reports whether cookies issued for this request must carry the Secure attribute:
// the operator-forced "https" scheme counts, as do direct TLS and a trusted proxy's
// X-Forwarded-Proto (see the package-level IsHTTPS for the precedence rules).
func (c *common) isHTTPS(r *http.Request) bool {
	return middleware.IsHTTPS(r, c.forcedScheme, c.trustedProxies)
}

// denyPrivate answers a caller that failed a resource's privacy check (domain.ErrForbidden from a
// usecase read). Shared by the note, link and file pages so their denial responses cannot drift.
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
		middleware.WriteUnauthorized(w)

		return
	}

	// Without accounts no login page exists, so a browser gets the plain 403 page. The usecases
	// do not refuse a read then (domain.Reader.SkipPrivacy), so only a usecase fault comes here.
	if c.accounts == accountsOff {
		c.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	if middleware.AuthenticatedFromCtx(r.Context()) {
		data := privateToOtherPageData()
		c.writeErrorPageData(w, r, &data)

		return
	}

	loginURL := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
}

// reader identifies the caller of r for the usecases. The auth middleware resolves the session
// or the API token of each record route and puts the user in the request context, so the
// context is the one source of the caller. AllowBurn is false: a handler sets it only for a
// deliberate access.
func (c *common) reader(r *http.Request) domain.Reader {
	return readerFromCtx(r.Context(), c.accounts)
}

// readerFromCtx is common.reader for the ogen handlers, which get only a context.
func readerFromCtx(ctx context.Context, accounts accountMode) domain.Reader {
	return domain.Reader{
		CallerID:      middleware.CallerIDFromCtx(ctx),
		Authenticated: middleware.AuthenticatedFromCtx(ctx),
		AllowBurn:     false,
		SkipPrivacy:   accounts == accountsOff,
	}
}

// writeMutationError answers a failed PUT or DELETE of a note, a short link or a file. A caller
// that cannot see the record gets 401, as for a read: a 403 would count as a wrong edit code for
// the fail lockout, and the edit-code check must not tell a stranger that the record exists.
func (c *common) writeMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		middleware.WriteUnauthorized(w)

		return
	}

	c.writeError(w, r, err)
}

// parseTmpl parses a page template together with the shared header partial.
func parseTmpl(name, src string) *template.Template {
	return template.Must(
		template.Must(template.New(name).Funcs(template.FuncMap{
			"styleVersion": func() string { return styleVersion },
			"assetVersion": assetVersions(),
			"initial":      initial,
		}).Parse(src)).Parse(headerTmplSrc),
	)
}

// assetVersions returns the template function that gives the cache-busting version of a file in
// static/, with the same scheme as styleVersion. Each file is hashed once, on first use. Templates
// execute concurrently, thus a mutex guards the cache.
func assetVersions() func(name string) string {
	var lock sync.Mutex

	versions := make(map[string]string)

	return func(name string) string {
		lock.Lock()
		defer lock.Unlock()

		version, ok := versions[name]
		if !ok {
			version = staticAssetVersion("static/" + name)
			versions[name] = version
		}

		return version
	}
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

	// files is not embedded: its RegisterRoutes and Delete would be ambiguous with the methods
	// of the other concern handlers.
	files *FileHandler

	// slugs tells GET /{id} and GET /edit/{id} which Manager has the slug (note_or_link.go).
	slugs SlugResolver
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager NoteManager, linkManager LinkManager, resolver SlugResolver, log *slog.Logger) *Handler {
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
	linkEditor := parseTmpl("link_editor", linkEditorTmplSrc)
	linkView := parseTmpl("link", linkTmplSrc)

	cmn := &common{
		log:       log,
		errorTmpl: errorTmpl,
		// sessions holds disabledAccounts and accounts is off until WithAuthManager; forcedScheme,
		// csrfSecret, trustedProxies, sessionTTL are zero-valued here and attached later via
		// WithForcedScheme / WithCSRFSecret /
		// WithTrustedProxies / WithSessionTTL from the composition root. NewRouter calls all
		// of them before the server starts serving requests — missing one is a startup bug,
		// caught on guarded POSTs (csrfSecret=nil → CSRFGuard rejects with 403).
		sessionChecker: disabledAccounts{},
		accounts:       accountsOff,
		forcedScheme:   "",
		csrfSecret:     nil,
		trustedProxies: nil,
		sessionTTL:     0,
		// Set by WithBootstrapToken; nil keeps first-admin setup closed.
		bootstrapDigest: nil,
		// Set by WithFileManager; nil keeps the file pages off.
		fileMgr: nil,
	}

	noteHandler := newNoteHandler(cmn, manager, noteTmpl, indexTmpl)

	return &Handler{
		common:       cmn,
		NoteHandler:  noteHandler,
		LinkHandler:  newLinkHandler(cmn, linkManager, redirectTmpl, linkEditor, linkView),
		AuthHandler:  newAuthHandler(cmn, loginTmpl, setupTmpl, changePwTmpl),
		AdminHandler: newAdminHandler(cmn, adminTmpl),
		PageHandler:  newPageHandler(cmn, indexTmpl, successTmpl, apidocsTmpl),
		// Files share the edit-code lockout with notes and links: one slug namespace.
		files: newFileHandler(cmn, noteHandler.lockout),
		slugs: resolver,
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
	h.sessionChecker = mgr
	h.accounts = accountsOn
	h.sessions = mgr
	h.AuthHandler.onboarding = mgr
	h.users = mgr
	h.tokens = mgr
	h.AdminHandler.onboarding = mgr

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
// IsHTTPS), since behind a TLS-terminating proxy that is the only signal the server has. Pass an
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
