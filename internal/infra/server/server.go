package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uptrace/bun"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/errorcontext"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/linkmetrics"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/rendercache"
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 5 * time.Second

	// idleTimeout bounds keep-alive idle time and is set explicitly on every listener.
	// With the field unset the stdlib falls back to ReadTimeout, which would leave the
	// auxiliary redirect and metrics listeners (ReadTimeout unset) with no idle limit at
	// all while the main server silently uses --read-timeout — two different policies by
	// accident rather than by choice.
	idleTimeout = 60 * time.Second
	// minCSRFSecretLen is the minimum accepted length for --csrf-secret; it matches the
	// 32-byte secret NewRouter generates when the flag is empty.
	minCSRFSecretLen = 32
	// minBootstrapTokenLen is the minimum accepted length for PADMARK_BOOTSTRAP_TOKEN: the token
	// alone decides who becomes the single admin, so it must not be guessable.
	minBootstrapTokenLen = 32
)

// parseBootstrapToken validates the PADMARK_BOOTSTRAP_TOKEN value. Empty is valid and keeps
// first-admin setup closed (fail-closed: without proof of server ownership the first visitor of
// /setup would become the permanent admin). A configured token shorter than the minimum fails
// startup rather than leaving the bootstrap guessable. The value never appears in errors or logs.
func parseBootstrapToken(raw string) (string, error) {
	if raw != "" && len(raw) < minBootstrapTokenLen {
		return "", fmt.Errorf("%w: %s must be at least %d characters, got %d",
			errInvalidBootstrapToken, EnvBootstrapToken, minBootstrapTokenLen, len(raw))
	}

	return raw, nil
}

// parseCSRFSecret converts the --csrf-secret flag value into an HMAC key. An empty value is
// valid and yields nil, meaning "generate a fresh random secret per process" (the legacy
// behaviour): single-instance deployments rely on it to avoid secret management, and the
// restart-invalidates-tokens tradeoff is acceptable there. A configured secret must be long
// enough to serve as an HMAC-SHA256 key; a short one would make the CSRF token forgeable in
// principle, so this fails at startup rather than weakening protection silently.
func parseCSRFSecret(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}

	if len(raw) < minCSRFSecretLen {
		return nil, fmt.Errorf("%w: csrf secret must be at least %d "+
			"characters, got %d", errInvalidCSRFSecret, minCSRFSecretLen, len(raw))
	}

	return []byte(raw), nil
}

func serveCommand() *cli.Command {
	return newCommand("serve", "Start the HTTP server", appFlags(), serverAction)
}

func parseTokens(raw string) []string {
	var result []string

	for part := range strings.SplitSeq(raw, ",") {
		if tok := strings.TrimSpace(part); tok != "" {
			result = append(result, tok)
		}
	}

	return result
}

// sessionTTL resolves the session lifetime from --session-ttl and the deprecated
// --cookie-max-age (the v0.1.6 name for the same knob). An explicit --session-ttl always wins and
// the deprecated value is ignored with a warning; otherwise a set --cookie-max-age is honoured
// so an upgrade does not silently change the lifetime operators configured. No extra bounds are
// applied: --session-ttl itself has none, and auth.NewManager rejects an unusable TTL.
// sessionTTL resolves the session lifetime: --session-ttl, or the deprecated v0.1.6
// --cookie-max-age when only that one is set. It is pure so that both consumers (router options
// and the auth manager) agree; warnDeprecatedSessionFlags reports the fallback once at startup.
func sessionTTL(cmd *cli.Command) time.Duration {
	seconds := cmd.Int(FlagSessionTTL)

	if cmd.IsSet(FlagCookieMaxAge) && !cmd.IsSet(FlagSessionTTL) {
		seconds = cmd.Int(FlagCookieMaxAge)
	}

	return time.Duration(seconds) * time.Second
}

// warnDeprecatedSessionFlags logs, once, how the deprecated --cookie-max-age was treated.
func warnDeprecatedSessionFlags(ctx context.Context, cmd *cli.Command, log *slog.Logger) {
	if !cmd.IsSet(FlagCookieMaxAge) {
		return
	}

	if cmd.IsSet(FlagSessionTTL) {
		log.WarnContext(ctx, "deprecated --cookie-max-age is ignored because --session-ttl is set",
			"flag", "--"+FlagCookieMaxAge, "env", EnvCookieMaxAge, "use", "--"+FlagSessionTTL)

		return
	}

	log.WarnContext(ctx, "--cookie-max-age is deprecated; its value is used as the session TTL, "+
		"switch to --session-ttl",
		"flag", "--"+FlagCookieMaxAge, "env", EnvCookieMaxAge, "use", "--"+FlagSessionTTL)
}

func buildRouter(
	ctx context.Context,
	cmd *cli.Command,
	handler *web.Handler,
	ogenHandler *web.OgenHandler,
	log *slog.Logger,
	bootstrapToken string,
) (http.Handler, error) {
	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return nil, fmt.Errorf("parse trusted proxies: %w", err)
	}

	// A warning rather than a startup error: the flag is still parseable and the operator may
	// have a reason we cannot see (a lab, a closed network). But an entry this wide trusts
	// arbitrary clients to name their own address via X-Real-IP, which retires the per-IP rate
	// limit — including the TOTP login cap — so it must never pass unremarked.
	if broad := overlyBroadProxyCIDRs(trustedProxies); len(broad) > 0 {
		log.WarnContext(ctx,
			"trusted proxy range covers arbitrary clients: any client can then set its own "+
				"X-Real-IP and get a fresh rate-limit bucket per request, defeating the per-IP "+
				"limits; narrow this to the proxy's actual addresses",
			"cidrs", strings.Join(broad, ","), "flag", "--"+FlagTrustedProxies,
		)
	}

	publicScheme, err := parsePublicScheme(cmd.String(FlagPublicScheme))
	if err != nil {
		return nil, fmt.Errorf("parse public scheme: %w", err)
	}

	csrfSecret, err := parseCSRFSecret(cmd.String(FlagCSRFSecret))
	if err != nil {
		return nil, fmt.Errorf("parse CSRF secret: %w", err)
	}

	// The per-process fallback is a deliberate, documented default rather than a startup failure:
	// a single instance works out of the box with no secret to manage, and its only cost is that
	// forms opened before a restart must be reloaded. It is silently wrong for anything larger,
	// though — every replica would reject the others' form tokens — so say so once at startup.
	if len(csrfSecret) == 0 {
		log.WarnContext(ctx,
			"no csrf secret configured: using a random one for this process only; "+
				"restarts invalidate open forms and multiple replicas reject each other's forms",
			"flag", "--"+FlagCSRFSecret, "env", EnvCSRFSecret,
		)
	}

	warnDeprecatedSessionFlags(ctx, cmd, log)
	opts := web.RouterOptions{
		MaxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		RateLimit:      cmd.Int(FlagRateLimit),
		RateBurst:      cmd.Int(FlagRateBurst),
		TrustedProxies: trustedProxies,
		ForcedScheme:   publicScheme,
		// Same flag attachAccountSystem feeds into auth.NewManager's sessionTTL, so the TOTP
		// session cookie's Max-Age always matches the actual server-side session lifetime.
		SessionTTL:     sessionTTL(cmd),
		DisableAPI:     cmd.Bool(FlagDisableAPI),
		DirectRedirect: cmd.Bool(FlagDirectRedirect),
		// The same allow-list guards the redirect listener and, here, the main one.
		AllowedHosts: parseTokens(cmd.String(FlagAllowedHosts)),
		// Nil (flag empty) lets NewRouter generate a fresh CSRF secret instead of accepting
		// an injected one — the legacy single-instance behaviour.
		CSRFSecret: csrfSecret,
		// Validated by serverAction before the DB opens; empty keeps first-admin setup closed.
		BootstrapToken: bootstrapToken,
	}

	router, err := web.NewRouter(handler, ogenHandler, &opts)
	if err != nil {
		return nil, fmt.Errorf("new router: %w", err)
	}

	return router, nil
}

// httpRedirectHandler redirects HTTP requests to their HTTPS equivalent.
// When allowedHosts is non-empty, a request whose Host is not in the set is rejected with
// 400 rather than redirected — this prevents Host-header injection from making the listener
// emit a redirect to an attacker-chosen host (open redirect / shared-cache poisoning).
// When empty, the request Host is used as-is (legacy behaviour for single-host deployments).
func httpRedirectHandler(allowedHosts map[string]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowedHosts) > 0 {
			hostname := r.Host

			host, _, splitErr := net.SplitHostPort(r.Host)
			if splitErr == nil {
				hostname = host
			}

			if _, ok := allowedHosts[hostname]; !ok {
				http.Error(w, "unknown host", http.StatusBadRequest)

				return
			}
		}

		target := "https://" + r.Host + r.URL.RequestURI()
		//nolint:gosec // G710: Host allowlisted above when configured
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// allowedHostSet parses a comma-separated host list into a set for the redirect allowlist.
// Returns nil when raw is empty, which disables enforcement.
func allowedHostSet(raw string) map[string]struct{} {
	hosts := parseTokens(raw)
	if len(hosts) == 0 {
		return nil
	}

	set := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		set[host] = struct{}{}
	}

	return set
}

// startRedirectServer launches the optional plain-HTTP→HTTPS redirect listener. Its failure is
// non-fatal: the redirect listener is a convenience, so a bind error (e.g. :80 already in use)
// is logged and swallowed rather than cancelling the main server's context — a healthy HTTPS
// server must not be torn down because the auxiliary redirector could not start.
// newRedirectServer builds the plain-HTTP→HTTPS redirect listener. Read and Write timeouts
// stay unset: the handler writes one redirect line per request and never reads a body, while
// the shared ReadHeaderTimeout/IdleTimeout constants still bound headers and keep-alive.
func newRedirectServer(addr string, allowedHosts map[string]struct{}) *http.Server {
	return newHTTPServer(addr, httpRedirectHandler(allowedHosts))
}

// newHTTPServer sets the address, the handler and the shared ReadHeaderTimeout/IdleTimeout
// policy. The other http.Server fields keep their zero values, so the stdlib defaults apply.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	server := new(http.Server)
	server.Addr = addr
	server.Handler = handler
	server.ReadHeaderTimeout = readHeaderTimeout
	server.IdleTimeout = idleTimeout

	return server
}

func startRedirectServer(ctx context.Context, addr string, allowedHosts map[string]struct{}, log *slog.Logger) {
	srv := newRedirectServer(addr, allowedHosts)

	compat.Append(shutdown.ContextFn(srv.Shutdown))

	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.ErrorContext(ctx, "http redirect listener stopped (main server unaffected)",
				"addr", addr, "err", err)
		}
	}()
}

// startMetricsServer launches the optional Prometheus /metrics listener on its own address,
// separate from the main public-facing router — see ~/.claude/docs/observability.md's "separate
// internal port" recommendation. Mirrors startRedirectServer's shape and non-fatal-failure
// handling exactly: a bind error here must not tear down an otherwise healthy main server.
// newMetricsServer builds the internal Prometheus /metrics listener — same timeout policy as
// the redirect listener: the shared ReadHeaderTimeout/IdleTimeout constants and no Read/Write
// timeouts (single small response, no request body).
func newMetricsServer(addr string, reg *prometheus.Registry) *http.Server {
	mux := http.NewServeMux()

	// The zero value selects the promhttp defaults.
	var metricsOpts promhttp.HandlerOpts

	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, metricsOpts))

	return newHTTPServer(addr, mux)
}

func startMetricsServer(ctx context.Context, addr string, reg *prometheus.Registry, log *slog.Logger) {
	srv := newMetricsServer(addr, reg)

	compat.Append(shutdown.ContextFn(srv.Shutdown))

	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.ErrorContext(ctx, "metrics listener stopped (main server unaffected)", "addr", addr, "err", err)
		}
	}()
}

func listenAndServe(srv *http.Server, tlsCert, tlsKey string, cancel context.CancelCauseFunc) {
	var serveErr error

	if tlsCert != "" {
		serveErr = srv.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		serveErr = srv.ListenAndServe()
	}

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		cancel(serveErr)
	} else {
		cancel(nil)
	}
}

// newMainServer builds the primary public-facing server. Read/Write timeouts and the header
// limit come from operator flags; ReadHeaderTimeout and IdleTimeout are shared constants so
// the main listener enforces the same policy as the auxiliary redirect and metrics listeners.
func newMainServer(addr string, router http.Handler, readTimeout, writeTimeout, maxHeaderBytes int) *http.Server {
	server := newHTTPServer(addr, router)
	server.ReadTimeout = time.Duration(readTimeout) * time.Second
	server.WriteTimeout = time.Duration(writeTimeout) * time.Second
	server.MaxHeaderBytes = maxHeaderBytes

	return server
}

func runServer(ctx context.Context, cmd *cli.Command, router http.Handler, log *slog.Logger) error {
	tlsCert := cmd.String(FlagTLSCert)
	tlsKey := cmd.String(FlagTLSKey)

	if (tlsCert == "") != (tlsKey == "") {
		return errIncompleteTLSConfig
	}

	srv := newMainServer(
		cmd.String(FlagAddr), router,
		cmd.Int(FlagReadTimeout), cmd.Int(FlagWriteTimeout), cmd.Int(FlagMaxHeaderBytes),
	)

	compat.Append(shutdown.ContextFn(srv.Shutdown))

	// serverCtx is cancelled with the server error if ListenAndServe fails unexpectedly,
	// allowing WaitForSignal to unblock and propagate the cause.
	serverCtx, cancelServer := context.WithCancelCause(ctx)
	defer cancelServer(nil)

	go listenAndServe(srv, tlsCert, tlsKey, cancelServer)

	if tlsCert != "" {
		if redirectAddr := cmd.String(FlagHTTPRedirectAddr); redirectAddr != "" {
			startRedirectServer(ctx, redirectAddr, allowedHostSet(cmd.String(FlagAllowedHosts)), log)
		}
	}

	return errorcontext.Wrap(waitAndShutdown(serverCtx), "wait and shutdown")
}

// waitAndShutdown blocks until SIGINT/SIGTERM arrives or serverCtx is cancelled (the main
// listener failed, or the parent context ended), then closes everything registered with the
// shared shutdown manager — the main, redirect and metrics listeners alike — under a bounded
// deadline. On the failure path the listener cause is joined with any shutdown error, so both
// stay reachable through errors.Is.
func waitAndShutdown(serverCtx context.Context) error {
	_, waitErr := compat.WaitForSignal(serverCtx, syscall.SIGINT, syscall.SIGTERM)

	// Start the deadline only after the wake-up, not at server startup. WithoutCancel
	// retains context values while letting shutdown outlive server cancellation.
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(serverCtx), shutdownTimeout)
	defer cancel()

	var shutdownErr error

	closeErr := compat.CloseContext(shutCtx)
	if closeErr != nil {
		shutdownErr = fmt.Errorf("graceful shutdown: %w", closeErr)
	}

	if waitErr != nil {
		return errors.Join(fmt.Errorf("server: %w", context.Cause(serverCtx)), shutdownErr)
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown server: %w", shutdownErr)
	}

	return nil
}

// attachAccountSystem wires the opt-in TOTP account manager onto the handler when
// --enable-accounts is set; otherwise it leaves the handler public (no authMgr → the auth
// middleware passes everything through).
//
// Returns an error when auth.NewManager's bootstrap-time crypto precompute fails — this only
// runs once at startup, so the caller should treat it as fatal.
func attachAccountSystem(
	ctx context.Context, cmd *cli.Command, handler *web.Handler, stores auth.Stores, log *slog.Logger,
) (*web.Handler, error) {
	if !cmd.Bool(FlagEnableAccounts) {
		log.InfoContext(ctx, "account system disabled (public mode); set --enable-accounts to enable login/admin")

		return handler, nil
	}

	ttl := sessionTTL(cmd)

	argonParams, err := argon2ParamsFromFlags(cmd)
	if err != nil {
		return nil, fmt.Errorf("argon2 params: %w", err)
	}

	authMgr, err := auth.NewManager(
		stores,
		crypto.NewPasswordHasher(argonParams).WithLogger(log),
		crypto.NewSealer(argonParams), crypto.NewTOTP(),
		log, cmd.String(FlagTOTPIssuer), ttl,
	)
	if err != nil {
		return nil, fmt.Errorf("new auth manager: %w", err)
	}

	empty, emptyErr := authMgr.IsEmpty(ctx)
	if emptyErr != nil {
		log.WarnContext(ctx, "check users empty", "err", emptyErr)
	} else if empty {
		// Presence only — the token itself is a secret and never logged.
		if os.Getenv(EnvBootstrapToken) == "" {
			log.WarnContext(ctx, "No users found, but first-admin setup is disabled: set the bootstrap "+
				"token env (at least 32 characters) and restart, then open /setup", "env", EnvBootstrapToken)
		} else {
			log.InfoContext(ctx, "No users found. Open /setup and enter the bootstrap token to create "+
				"the first admin; unset it afterwards", "env", EnvBootstrapToken)
		}
	}

	return handler.WithAuthManager(authMgr), nil
}

//nolint:ireturn // factory: returns different concrete impls (postgres vs sqlite) behind common interfaces
func buildAuthRepos(storage string, db *bun.DB) (auth.Stores, web.RevealTokenStore) {
	switch storage {
	case storagePostgres:
		return auth.Stores{
			Users:     postgres.NewUserRepository(db),
			Invites:   postgres.NewInviteRepository(db, slog.Default()),
			Sessions:  postgres.NewSessionRepository(db, slog.Default()),
			APITokens: postgres.NewAPITokenRepository(db),
		}, postgres.NewRevealRepository(db, slog.Default())
	default:
		return auth.Stores{
			Users:     sqlite.NewUserRepository(db),
			Invites:   sqlite.NewInviteRepository(db, slog.Default()),
			Sessions:  sqlite.NewSessionRepository(db, slog.Default()),
			APITokens: sqlite.NewAPITokenRepository(db),
		}, sqlite.NewRevealRepository(db, slog.Default())
	}
}

// argon2ParamsFromFlags reads the configurable argon2id cost from flags/env.
// Zero values fall back to the built-in defaults inside the crypto constructors.
func argon2ParamsFromFlags(cmd *cli.Command) (crypto.Argon2Params, error) {
	memory, memoryErr := uint32Flag(cmd, FlagArgon2Memory)
	timeCost, timeErr := uint32Flag(cmd, FlagArgon2Time)
	threads, threadsErr := uint8Flag(cmd, FlagArgon2Threads)

	flagErr := errors.Join(memoryErr, timeErr, threadsErr)
	if flagErr != nil {
		return crypto.Argon2Params{Memory: 0, Time: 0, Threads: 0}, fmt.Errorf("read argon2 flags: %w", flagErr)
	}

	return crypto.Argon2Params{Memory: memory, Time: timeCost, Threads: threads}, nil
}

// newMetricsRegistry builds the Prometheus registry backing the optional /metrics listener,
// pre-registered with the standard Go/process collectors plus the links.Metrics counters.
func newMetricsRegistry() (*prometheus.Registry, *linkmetrics.Metrics, error) {
	reg := prometheus.NewRegistry()

	// The zero value selects the collector defaults.
	var procOpts collectors.ProcessCollectorOpts

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(procOpts),
	)

	linkMetrics, err := linkmetrics.NewRegistered(reg)
	if err != nil {
		return nil, nil, fmt.Errorf("register link metrics: %w", err)
	}

	return reg, linkMetrics, nil
}

// serverStorage is the opened storage backend: its name, the connection and the content
// repositories that initRepos built for it.
type serverStorage struct {
	db    *bun.DB
	notes notes.Storage
	links links.Storage
	name  string
}

// buildHandlers assembles the notes/links business-logic managers and wires them into the
// JSON/HTML handler and the ogen-generated handler, including the optional account system and
// link cache/metrics collaborators.
func buildHandlers(
	ctx context.Context, cmd *cli.Command, store serverStorage, log *slog.Logger,
	linkMetrics *linkmetrics.Metrics, keyring *crypto.ContentKeyring,
) (*web.Handler, *web.OgenHandler, error) {
	noteCipher, err := keyring.ForResource("note")
	if err != nil {
		return nil, nil, fmt.Errorf("note content cipher: %w", err)
	}

	linkCipher, err := keyring.ForResource("link")
	if err != nil {
		return nil, nil, fmt.Errorf("link content cipher: %w", err)
	}

	argonParams, err := argon2ParamsFromFlags(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("argon2 params: %w", err)
	}

	var checker links.NotesChecker

	switch store.name {
	case storagePostgres:
		checker = newPostgresNotesChecker(store.db)
	default:
		checker = newSQLiteNotesChecker(store.db)
	}

	// One Locker instance shared by both managers: it closes the cross-table TOCTOU race
	// between notes.Manager.Create and links.Manager.Create (see notes.SlugLock's doc comment)
	// — a Locker per manager would each serialize its own calls but never contend with the
	// other's, protecting nothing.
	slugLock := lock.NewLocker()

	manager := notes.NewManager(store.notes, store.links, slugLock, render.NewRenderer(), noteCipher,
		crypto.NewEditCodeHasher(argonParams).WithLogger(log), log, cmd.Bool(FlagCustomSlugs)).
		WithRenderCache(rendercache.NewDefault(log))
	linkManager := links.NewManager(store.links, checker, slugLock, linkCipher,
		crypto.NewEditCodeHasher(argonParams).WithLogger(log), log, cmd.Bool(FlagCustomSlugs)).
		WithCache(linkcache.NewDefault(log)).
		WithMetrics(linkMetrics)

	authStores, revealStore := buildAuthRepos(store.name, store.db)

	handler := web.NewHandler(manager, linkManager, log).
		WithRevealStore(revealStore)

	handler, err = attachAccountSystem(ctx, cmd, handler, authStores, log)
	if err != nil {
		return nil, nil, fmt.Errorf("attach account system: %w", err)
	}

	ogenHandler := web.NewOgenHandler(manager, linkManager, store.db.DB, log)

	return handler, ogenHandler, nil
}

// initRepos runs migrations and builds the note and link repositories for the chosen backend.
// openDB already validated the backend name, so unknown values cannot reach the default branch.
func initRepos(
	ctx context.Context, storage string, db *bun.DB, log *slog.Logger, opts migrationOptions,
) (serverStorage, error) {
	store := serverStorage{db: db, notes: nil, links: nil, name: storage}

	switch storage {
	case storagePostgres:
		repo, err := initPostgresStorage(ctx, db, log)
		if err != nil {
			return store, fmt.Errorf("init postgres storage: %w", err)
		}

		store.notes, store.links = repo, newPostgresLinkRepository(db)
	default:
		repo, err := initSQLiteStorage(ctx, db, log, opts)
		if err != nil {
			return store, fmt.Errorf("init sqlite storage: %w", err)
		}

		store.notes, store.links = repo, newSQLiteLinkRepository(db)
	}

	return store, nil
}

func serverAction(ctx context.Context, cmd *cli.Command) error {
	bootstrapToken, err := validateStartupSecrets(cmd)
	if err != nil {
		return fmt.Errorf("validate startup secrets: %w", err)
	}

	// Bound concurrent argon2id workspace before any hasher can run (validated above).
	hasher, err := prepareArgon2(cmd)
	if err != nil {
		return fmt.Errorf("prepare argon2: %w", err)
	}

	log := newLogger(cmd.String(FlagLogLevel), cmd.String(FlagLogFormat))

	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

	keyring, err := loadServeKeyring(cmd.String(FlagContentKeyring), storage, dsn, cmd.Bool(FlagAllowSharedKeyring), log)
	if err != nil {
		return fmt.Errorf("load content encryption keys: %w", err)
	}

	db, err := openDB(ctx, storage, dsn, log)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	defer closeServerDB(ctx, db, log)

	store, err := initRepos(ctx, storage, db, log,
		migrationOptions{dsn: dsn, skipBackup: cmd.Bool(FlagSkipMigrationBackup)})
	if err != nil {
		return fmt.Errorf("init repos: %w", err)
	}

	err = validateContentEncryption(ctx, db, keyring, hasher, log)
	if err != nil {
		return fmt.Errorf("validate content encryption: %w", err)
	}

	metricsRegistry, linkMetrics, err := newMetricsRegistry()
	if err != nil {
		return fmt.Errorf("create metrics registry: %w", err)
	}

	handler, ogenHandler, err := buildHandlers(ctx, cmd, store, log, linkMetrics, keyring)
	if err != nil {
		return fmt.Errorf("build request handlers: %w", err)
	}

	router, err := buildRouter(ctx, cmd, handler, ogenHandler, log, bootstrapToken)
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	if metricsAddr := cmd.String(FlagMetricsAddr); metricsAddr != "" {
		startMetricsServer(ctx, metricsAddr, metricsRegistry, log)
	}

	logServerStarted(ctx, cmd, log, storage, dsn)

	return errorcontext.Wrap(runServer(ctx, cmd, router, log), "run server")
}

// logServerStarted records the listener, the storage backend and the redacted DSN at startup.
func logServerStarted(ctx context.Context, cmd *cli.Command, log *slog.Logger, storage, dsn string) {
	tlsEnabled := cmd.String(FlagTLSCert) != ""

	// The scheme reflects TLS on this listener, independently of a reverse proxy.
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	log.InfoContext(ctx, "server started",
		"addr", cmd.String(FlagAddr), "url", listenerURL(scheme, cmd.String(FlagAddr)),
		"storage", storage, "dsn", redactDSN(dsn),
		"tls", tlsEnabled,
	)
}

// errRemovedAuthTokens is returned when the removed --auth-tokens / PADMARK_AUTH_TOKENS is set.
var errRemovedAuthTokens = fmt.Errorf(
	"--%s / %s was removed in this version: static bearer tokens are replaced by accounts; "+
		"use --%s with %s and create the first admin via /setup or `padmark-cli setup` "+
		"(API tokens are issued via /api/tokens); unset the old variable to start",
	FlagAuthTokens, EnvAuthTokens, FlagEnableAccounts, EnvBootstrapToken,
)

// validateStartupSecrets checks the security configuration that must be sound before anything
// is opened: the argon2 cost bounds, the argon2 memory budget and the bootstrap token. It returns
// the validated token.
func validateStartupSecrets(cmd *cli.Command) (string, error) {
	// Fail closed: v0.1.6 gated writes behind these tokens, so ignoring them would bring the
	// server up with anonymous writes. Only whether the value is set is checked; it is never
	// echoed.
	if cmd.String(FlagAuthTokens) != "" {
		return "", errRemovedAuthTokens
	}

	argonErr := validateArgon2Config(cmd)
	if argonErr != nil {
		return "", fmt.Errorf("validate argon2 config: %w", argonErr)
	}

	bootstrapToken, tokenErr := parseBootstrapToken(os.Getenv(EnvBootstrapToken))
	if tokenErr != nil {
		return "", fmt.Errorf("validate bootstrap token: %w", tokenErr)
	}

	return bootstrapToken, nil
}

// validateArgon2Config checks the argon2 cost bounds and the memory budget (fail-hard).
func validateArgon2Config(cmd *cli.Command) error {
	params, paramsErr := argon2ParamsFromFlags(cmd)
	if paramsErr != nil {
		return fmt.Errorf("argon2 params: %w", paramsErr)
	}

	costErr := params.Validate()
	if costErr != nil {
		return fmt.Errorf("validate argon2 configuration: %w", costErr)
	}

	budgetErr := validateArgon2MemoryBudget(cmd)
	if budgetErr != nil {
		return fmt.Errorf("validate argon2 configuration: %w", budgetErr)
	}

	return nil
}

// validateArgon2MemoryBudget checks that the concurrency budget can hold at least one
// derivation at the configured password cost.
func validateArgon2MemoryBudget(cmd *cli.Command) error {
	budget, memory := cmd.Int(FlagArgon2MemoryBudget), cmd.Int(FlagArgon2Memory)
	if budget < memory {
		return fmt.Errorf("%w: --%s (%d KiB) must be at least --%s (%d "+
			"KiB)", errInvalidArgon2Budget, FlagArgon2MemoryBudget, budget, FlagArgon2Memory, memory)
	}

	return nil
}

func closeServerDB(ctx context.Context, db *bun.DB, log *slog.Logger) {
	err := db.Close()
	if err != nil {
		log.ErrorContext(ctx, "close db", "err", err)
	}
}
