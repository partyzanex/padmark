package web

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

// RouterOptions holds configurable parameters for the middleware stack.
type RouterOptions struct {
	ForcedScheme string
	// BootstrapToken is the operator's proof of server ownership for creating the first admin
	// via /setup without an invite (PADMARK_BOOTSTRAP_TOKEN). Empty keeps that bootstrap closed:
	// otherwise whoever reached /setup first would become the single, permanent admin.
	BootstrapToken string
	TrustedProxies []*net.IPNet
	CSRFSecret     []byte
	// AllowedHosts is the Host header allow-list for the main listener (--allowed-hosts). When
	// non-empty, any other Host is refused with 421 before a handler runs; empty disables the
	// check. See WithAllowedHosts.
	AllowedHosts []string
	MaxBodyBytes int
	RateLimit    int
	RateBurst    int
	// SessionTTL sets the TOTP session cookie's Max-Age (see AuthHandler), matching the
	// server-side session lifetime configured via auth.NewManager's sessionTTL — both should be
	// fed the same --session-ttl value so the cookie never outlives (or expires before) the
	// session it represents. Zero falls back to auth.DefaultSessionTTL.
	SessionTTL time.Duration
	// DisableAPI turns off the REST/JSON API surface (/notes*, /api, /api/openapi.yaml): every
	// request reaching the disabling middleware gets 503 with code=api_disabled. The web UI and
	// /healthz, /readyz keep working — see WithAPIDisabled.
	DisableAPI bool
	// DirectRedirect selects the short-link redirect's response mode: true issues an immediate 301 to the
	// short link's target URL, false renders a transparency interstitial. See LinkHandler.Redirect.
	DirectRedirect bool
}

// RouteRegistrar registers one concern's HTTP routes on mux. Each concern-specific handler
// (NoteHandler, AuthHandler, AdminHandler, PageHandler) implements it, so adding a route to an
// existing concern only touches that concern's own file, and adding a new concern handler means
// implementing this interface and appending it to the registrars slice in NewRouter — neither
// requires editing a shared, ever-growing route-registration function.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// registerOgenRoutes registers the generated REST API routes (note and link CRUD, health checks)
// served directly by ogenSrv. These aren't owned by any concern handler — ogenSrv wraps the
// separate OgenHandler — so they're registered here rather than via a RouteRegistrar. lockout is
// shared with NoteHandler's native {id...} routes (see NoteHandler.RegisterRoutes) so fail-lockout
// tracking is consistent for a note or link regardless of which route variant a client hits — a
// single cache keyed by path ID plus caller works since notes and links share one global slug
// namespace (a note and a link can never have the same ID, see domain.HashSlug/ErrSlugConflict).
//
// The /files operations are in openapi.yaml, but they are not registered here. The generated
// server parses the full multipart body (ParseMultipartForm: memory and temporary files) before
// the handler starts, and a file can be many gigabytes. FileHandler.RegisterRoutes serves
// /files* and streams the content to and from object storage. The generated file methods stay
// on ogenapi.UnimplementedHandler, which OgenHandler embeds; no route reaches them.
func registerOgenRoutes(
	mux *http.ServeMux, ogenSrv http.Handler, lockout *middleware.FailLockoutCache,
	trustedProxies []*net.IPNet, log *slog.Logger,
) {
	locked := func(next http.Handler) http.Handler {
		return middleware.RequireSameOrigin(log, middleware.WithFailLockout(lockout, trustedProxies, log, next))
	}

	mux.Handle("POST /notes", ogenSrv)
	mux.Handle("PUT /notes/{id}", locked(ogenSrv))
	mux.Handle("DELETE /notes/{id}", locked(middleware.WithLegacyNoteEditCode(ogenSrv)))
	mux.Handle("POST /links", ogenSrv)
	mux.Handle("POST /links/bulk", ogenSrv)
	// GET /links/{id} also serves the link page to a browser; registerAllRoutes registers it.
	mux.Handle("PUT /links/{id}", locked(ogenSrv))
	mux.Handle("DELETE /links/{id}", locked(ogenSrv))
	mux.Handle("GET /healthz", ogenSrv)
	mux.Handle("GET /readyz", ogenSrv)
	// Account endpoints and note/link PUT/DELETE accept browser session cookies, so they enforce
	// same-origin requests. POST /notes, POST /links and POST /links/bulk are deliberately not
	// wrapped: they accept only JSON bodies (a cross-site JSON POST forces a CORS preflight the
	// server never answers) and the session cookie is SameSite=Strict.
	mux.Handle("POST /api/tokens", middleware.RequireSameOrigin(log, ogenSrv))
	mux.Handle("POST /api/invites", middleware.RequireSameOrigin(log, ogenSrv))
}

// registerAllRoutes registers the ogen-served REST API routes plus every concern handler's own
// routes (see RouteRegistrar) on mux.
func registerAllRoutes(mux *http.ServeMux, handler *Handler, ogenSrv http.Handler) {
	registerOgenRoutes(mux, ogenSrv, handler.lockout, handler.trustedProxies, handler.log)

	registrars := []RouteRegistrar{
		handler.NoteHandler,
		handler.LinkHandler,
		handler.AuthHandler,
		handler.AdminHandler,
		handler.PageHandler,
		handler.files,
	}
	for _, registrar := range registrars {
		registrar.RegisterRoutes(mux)
	}

	// The link page shares its path with the JSON read, as GET /notes/{id} serves the note page
	// and the note JSON: a client that prefers HTML gets the page, other clients get the API.
	mux.Handle("GET /links/{id}", htmlOr(handler.LinkPage, ogenSrv))
	mux.HandleFunc("GET /edit/{id...}", handler.EditPage)
	mux.HandleFunc("GET /{id...}", handler.GetNoteOrLink)
}

// htmlOr sends a request that prefers HTML (see negotiate) to page and other requests to api.
func htmlOr(page http.HandlerFunc, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if negotiate(r) == formatHTML {
			page(w, r)

			return
		}

		api.ServeHTTP(w, r)
	})
}

// isLinkPageRequest reports whether r asks for the page of a short link: GET or HEAD
// /links/{id} from a client that prefers HTML. It is the rule of htmlOr for that path. With
// --disable-api the page stays available, as the note page /{id} does, and the JSON read stops.
func isLinkPageRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	slug, found := strings.CutPrefix(r.URL.Path, "/links/")

	return found && slug != "" && !strings.Contains(slug, "/") && negotiate(r) == formatHTML
}

// NewRouter registers all routes and wraps them with middleware.
//
// Returns an error when building the generated ogen server fails, or when crypto/rand is
// unavailable to generate a CSRF secret (only when opts.CSRFSecret is empty). NewRouter runs
// only at the composition root, so the caller should treat either as a fatal startup error.
func NewRouter(
	handler *Handler, ogenHandler *OgenHandler, opts *RouterOptions,
) (http.Handler, error) {
	// The auth seam is optional on OgenHandler; attach it where it exists so the account
	// endpoints (POST /api/tokens, /api/invites) serve instead of 503-ing.
	apiHandler := ogenHandler
	if handler.authMgr != nil {
		apiHandler = ogenHandler.WithAuthManager(handler.authMgr)
	}

	ogenSrv, err := ogenapi.NewServer(apiHandler, ogenapi.WithErrorHandler(newOgenErrorHandler(handler.log)))
	if err != nil {
		return nil, fmt.Errorf("new ogen server: %w", err)
	}

	const csrfSecretSize = 32

	csrfSecret := opts.CSRFSecret
	if len(csrfSecret) == 0 {
		csrfSecret = make([]byte, csrfSecretSize)

		_, randErr := rand.Read(csrfSecret)
		if randErr != nil {
			return nil, fmt.Errorf("generate csrf secret: %w", randErr)
		}
	}

	handler.WithCSRFSecret(csrfSecret)
	handler.WithTrustedProxies(opts.TrustedProxies)
	handler.WithForcedScheme(opts.ForcedScheme)
	handler.WithSessionTTL(opts.SessionTTL)
	handler.WithDirectRedirect(opts.DirectRedirect)
	handler.WithBootstrapToken(opts.BootstrapToken)

	mux := http.NewServeMux()

	registerAllRoutes(mux, handler, ogenSrv)

	return wrapMiddleware(mux, handler, opts, csrfSecret), nil
}

// wrapMiddleware wraps the routed mux in the server's middleware chain. The listing order is
// innermost-first: the last wrapper applied is the one that sees a request first.
func wrapMiddleware(
	mux http.Handler, handler *Handler, opts *RouterOptions, csrfSecret []byte,
) http.Handler {
	var checker middleware.SessionChecker
	if handler.authMgr != nil {
		checker = handler.authMgr
	}

	stack := middleware.WithRecovery(handler.log, mux)
	stack = middleware.WithRequestOrigin(opts.ForcedScheme, opts.TrustedProxies, stack)

	if opts.DisableAPI {
		stack = middleware.WithAPIDisabled(handler.log, isLinkPageRequest, stack)
	}

	stack = middleware.NewAuthMiddleware(buildNamedRoutes(), checker, stack)
	stack = middleware.WithCSRFToken(csrfSecret, opts.ForcedScheme, opts.TrustedProxies, stack)

	stack = middleware.WithBodyLimitExcept(int64(opts.MaxBodyBytes), isFileUpload, stack)

	if opts.RateLimit > 0 {
		stack = middleware.WithRateLimit(opts.RateLimit, opts.RateBurst, opts.TrustedProxies, handler.log, stack)
	}

	// Only meaningful when nothing is trusted — see WarnUnconfiguredProxy.
	if len(opts.TrustedProxies) == 0 {
		stack = middleware.WarnUnconfiguredProxy(handler.log, stack)
	}

	// Outside every stateful layer (auth, CSRF, rate limit) so a forged Host is refused before
	// any of them runs, yet inside security headers, logging and the request ID so the refusal
	// is still logged and carries them.
	stack = middleware.WithAllowedHosts(opts.AllowedHosts, stack)

	stack = middleware.WithSecurityHeaders(opts.ForcedScheme, opts.TrustedProxies, stack)
	stack = middleware.WithLogging(handler.log, stack)
	stack = middleware.WithRequestID(stack)

	// Second, outermost recovery layer: the inner WithRecovery above only covers mux, so a
	// panic in any middleware between it and here (auth, CSRF, rate-limit, security-headers,
	// logging, request-ID) would otherwise fall through to net/http's bare per-connection
	// recovery, which drops the response body and skips structured logging entirely.
	return middleware.WithRecovery(handler.log, stack)
}

// buildNamedRoutes returns the set of first path segments that name built-in routes rather than
// notes or links. The auth middleware treats any request whose first segment is in this set as
// a non-note route (so it keeps requiring auth); everything else may be a public note view.
// Sourced from domain.ReservedSlugPrefixes, the single source of truth also used to reject
// colliding custom slugs (shared by notes.Manager and links.Manager — a usecase package may not
// import another usecase package, so this can't live in either directly); keep the route
// registrations in NewRouter in sync with it.
// login/setup/logout/totp-login/healthz/readyz are also special-cased by exact path in
// isPublicPath, but stay listed here too: domain.ReservedSlugPrefixes blocks the whole
// first-segment namespace from custom-slug collisions (e.g. "healthz/status"), not just those
// literal paths.
func buildNamedRoutes() map[string]struct{} {
	return domain.ReservedSlugPrefixes()
}
