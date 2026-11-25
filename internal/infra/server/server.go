package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uptrace/bun"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/filemetrics"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/linkmetrics"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/rendercache"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	readHeaderTimeout = 5 * time.Second

	// idleTimeout bounds keep-alive idle time and is set explicitly on every listener.
	// With the field unset the stdlib falls back to ReadTimeout, which would leave the
	// auxiliary redirect and metrics listeners (ReadTimeout unset) with no idle limit at
	// all while the main server silently uses --read-timeout — two different policies by
	// accident rather than by choice.
	idleTimeout = 60 * time.Second
)

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

// warnDeprecatedSessionFlags logs, once, how the deprecated --cookie-max-age was treated.
func warnDeprecatedSessionFlags(ctx context.Context, accounts accountsConfig, log *slog.Logger) {
	if !accounts.cookieMaxAgeSet {
		return
	}

	if accounts.sessionTTLSet {
		log.WarnContext(ctx, "deprecated --cookie-max-age is ignored because --session-ttl is set",
			"flag", "--"+FlagCookieMaxAge, "env", EnvCookieMaxAge, "use", "--"+FlagSessionTTL)

		return
	}

	log.WarnContext(ctx, "--cookie-max-age is deprecated; its value is used as the session TTL, "+
		"switch to --session-ttl",
		"flag", "--"+FlagCookieMaxAge, "env", EnvCookieMaxAge, "use", "--"+FlagSessionTTL)
}

// buildRouter makes the router from the checked configuration. It logs the warnings about the
// proxy, CSRF and session settings once at startup.
func buildRouter(ctx context.Context, cfg *Config, handler *web.Handler, log *slog.Logger) (http.Handler, error) {
	// A warning rather than a startup error: the flag is still parseable and the operator may
	// have a reason we cannot see (a lab, a closed network). But an entry this wide trusts
	// arbitrary clients to name their own address via X-Real-IP, which retires the per-IP rate
	// limit — including the TOTP login cap — so it must never pass unremarked.
	if broad := overlyBroadProxyCIDRs(cfg.router.trustedProxies); len(broad) > 0 {
		log.WarnContext(ctx,
			"trusted proxy range covers arbitrary clients: any client can then set its own "+
				"X-Real-IP and get a fresh rate-limit bucket per request, defeating the per-IP "+
				"limits; narrow this to the proxy's actual addresses",
			"cidrs", strings.Join(broad, ","), "flag", "--"+FlagTrustedProxies,
		)
	}

	// The per-process fallback is a deliberate, documented default rather than a startup failure:
	// a single instance works out of the box with no secret to manage, and its only cost is that
	// forms opened before a restart must be reloaded. It is silently wrong for anything larger,
	// though — every replica would reject the others' form tokens — so say so once at startup.
	if len(cfg.router.csrfSecret) == 0 {
		log.WarnContext(ctx,
			"no csrf secret configured: using a random one for this process only; "+
				"restarts invalidate open forms and multiple replicas reject each other's forms",
			"flag", "--"+FlagCSRFSecret, "env", EnvCSRFSecret,
		)
	}

	warnDeprecatedSessionFlags(ctx, cfg.accounts, log)

	opts := web.RouterOptions{
		// The same allow-list guards the redirect listener and, here, the main one.
		AllowedHosts: cfg.http.allowedHosts,
		MaxBodyBytes: cfg.router.maxBodyBytes,
		RateLimit:    cfg.router.rateLimit,
		RateBurst:    cfg.router.rateBurst,
		DisableAPI:   cfg.router.disableAPI,
	}

	router, err := web.NewRouter(handler, &opts)
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

// startRedirectServer launches the optional plain-HTTP→HTTPS redirect listener. Its failure is
// non-fatal: the redirect listener is a convenience, so a bind error (e.g. :80 already in use)
// is logged and swallowed rather than cancelling the main server's context — a healthy HTTPS
// server must not be torn down because the auxiliary redirector could not start.
func startRedirectServer(
	ctx context.Context, addr string, allowedHosts map[string]struct{}, stop *serverStop, log *slog.Logger,
) {
	srv := newRedirectServer(addr, allowedHosts)

	stop.addListener(ctx, "redirect", srv)

	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.ErrorContext(ctx, "http redirect listener stopped (main server unaffected)",
				"addr", addr, "err", err)
		}
	}()
}

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

// startMetricsServer launches the optional Prometheus /metrics listener on its own address,
// separate from the main public-facing router (an internal port). Mirrors startRedirectServer's
// shape and non-fatal-failure handling exactly: a bind error here must not tear down an
// otherwise healthy main server.
func startMetricsServer(
	ctx context.Context, addr string, reg *prometheus.Registry, stop *serverStop, log *slog.Logger,
) {
	srv := newMetricsServer(addr, reg)

	stop.addListener(ctx, "metrics", srv)

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

// runServer starts the main listener and the optional redirect listener, then waits for a
// signal or a listener failure and runs the shutdown sequence.
func runServer(ctx context.Context, cfg *Config, router http.Handler, stop *serverStop, log *slog.Logger) error {
	srv := newMainServer(cfg.http.addr, router, cfg.http.readTimeout, cfg.http.writeTimeout, cfg.http.maxHeaderBytes)

	stop.addListener(ctx, "main", srv)

	// serverCtx is cancelled with the server error if ListenAndServe fails unexpectedly,
	// allowing WaitForSignal to unblock and propagate the cause.
	serverCtx, cancelServer := context.WithCancelCause(ctx)
	defer cancelServer(nil)

	go listenAndServe(srv, cfg.http.tlsCert, cfg.http.tlsKey, cancelServer)

	if cfg.http.tlsCert != "" && cfg.http.redirectAddr != "" {
		startRedirectServer(ctx, cfg.http.redirectAddr, cfg.http.redirectHosts, stop, log)
	}

	return waitAndShutdown(serverCtx, stop)
}

// attachAccountSystem puts the opt-in TOTP account manager into handlerCfg.Accounts when
// --enable-accounts is set; otherwise it leaves the field nil, and the handler stays public (the
// auth middleware passes everything through). It runs before web.NewHandler, so the handler is
// built once with its final account system.
//
// Returns an error when auth.NewManager's bootstrap-time crypto precompute fails — this only
// runs once at startup, so the caller should treat it as fatal.
func attachAccountSystem(
	ctx context.Context, cfg *Config, handlerCfg *web.HandlerConfig, stores auth.Stores, log *slog.Logger,
) error {
	if !cfg.accounts.enabled {
		log.InfoContext(ctx, "account system disabled (public mode); set --enable-accounts to enable login/admin")

		return nil
	}

	authMgr, err := auth.NewManager(
		stores,
		&auth.Deps{
			PasswordHasher: crypto.NewPasswordHasher(cfg.argon2.params, log),
			Sealer:         crypto.NewSealer(cfg.argon2.params),
			TOTP:           crypto.NewTOTP(),
			Log:            log,
			Now:            time.Now,
		},
		cfg.accounts.totpIssuer, cfg.accounts.sessionTTL,
	)
	if err != nil {
		return fmt.Errorf("new auth manager: %w", err)
	}

	empty, emptyErr := authMgr.IsEmpty(ctx)
	if emptyErr != nil {
		log.WarnContext(ctx, "check users empty", "err", emptyErr)
	} else if empty {
		// Presence only — the token itself is a secret and never logged.
		if cfg.accounts.bootstrapToken == "" {
			log.WarnContext(ctx, "No users found, but first-admin setup is disabled: set the bootstrap "+
				"token env (at least 32 characters) and restart, then open /setup", "env", EnvBootstrapToken)
		} else {
			log.InfoContext(ctx, "No users found. Open /setup and enter the bootstrap token to create "+
				"the first admin; unset it afterwards", "env", EnvBootstrapToken)
		}
	}

	handlerCfg.Accounts = authMgr

	return nil
}

// serverMetrics holds the Prometheus registry and the metrics of the managers in it.
type serverMetrics struct {
	registry *prometheus.Registry
	links    *linkmetrics.Metrics
	files    *filemetrics.Metrics
}

// newMetricsRegistry builds the Prometheus registry backing the optional /metrics listener,
// pre-registered with the standard Go/process collectors plus the links.Metrics and
// files.Metrics collectors. The file metrics are registered also when files are off: then they
// stay at zero, and the /metrics output has the same names on each server.
func newMetricsRegistry() (serverMetrics, error) {
	reg := prometheus.NewRegistry()

	// The zero value selects the collector defaults.
	var procOpts collectors.ProcessCollectorOpts

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(procOpts),
	)

	linkMetrics, err := linkmetrics.NewRegistered(reg)
	if err != nil {
		return serverMetrics{registry: nil, links: nil, files: nil}, fmt.Errorf("register link metrics: %w", err)
	}

	fileMetrics, err := filemetrics.NewRegistered(reg)
	if err != nil {
		return serverMetrics{registry: nil, links: nil, files: nil}, fmt.Errorf("register file metrics: %w", err)
	}

	return serverMetrics{registry: reg, links: linkMetrics, files: fileMetrics}, nil
}

// contentServices are the objects that serve builds one time and gives to more than one
// manager: the content keyring, the edit-code hasher and the stop of the background work.
type contentServices struct {
	keyring    *crypto.ContentKeyring
	hasher     *crypto.EditCodeHasher
	background *shutdown.Lifo
}

// buildHandlers assembles the notes/links business-logic managers and the optional account
// system and file manager, and builds the HTTP handler from them in one web.NewHandler call.
func buildHandlers(
	ctx context.Context, cfg *Config, store *serverStorage, shared contentServices,
	metrics serverMetrics, log *slog.Logger,
) (*web.Handler, error) {
	noteCipher, err := shared.keyring.ForResource("note")
	if err != nil {
		return nil, fmt.Errorf("note content cipher: %w", err)
	}

	linkCipher, err := shared.keyring.ForResource("link")
	if err != nil {
		return nil, fmt.Errorf("link content cipher: %w", err)
	}

	// The storage triggers keep slugs unique across notes, links and files, also between server
	// processes. The managers share one expiredslugs.Releaser, so an expired record of any kind
	// does not block a new record with its slug.
	expired := expiredslugs.New(store.notes, store.links)

	manager := notes.NewManager(&notes.Deps{
		Storage: store.notes, Renderer: render.NewRenderer(), Encryptor: noteCipher,
		Hasher: shared.hasher, ExpiredSlugs: expired, Log: log, Now: time.Now,
	}, cfg.maxContentLength, cfg.customSlugs).
		WithRenderCache(rendercache.NewDefault(log))
	linkManager := links.NewManager(&links.Deps{
		Storage: store.links, Encryptor: linkCipher,
		Hasher: shared.hasher, ExpiredSlugs: expired, Log: log, Now: time.Now,
	}, cfg.customSlugs).
		WithCache(linkcache.NewDefault(log)).
		WithMetrics(metrics.links)

	handlerCfg := web.HandlerConfig{
		Notes: manager, Links: linkManager, Pinger: store.db.DB, Log: log,
		// attachAccountSystem and attachFiles set these when their feature is on.
		Accounts: nil, Files: nil,
		Reveal:       auth.NewRevealManager(store.reveal, log, time.Now),
		ForcedScheme: cfg.router.publicScheme,
		// Empty keeps first-admin setup closed.
		BootstrapToken: cfg.accounts.bootstrapToken,
		TrustedProxies: cfg.router.trustedProxies,
		// Nil (flag empty) lets NewHandler generate a fresh CSRF secret instead of accepting
		// an injected one — the legacy single-instance behaviour.
		CSRFSecret: cfg.router.csrfSecret,
		// The auth manager gets the same value, so the TOTP session cookie's Max-Age always
		// matches the actual server-side session lifetime.
		SessionTTL:     cfg.accounts.sessionTTL,
		FileTransfer:   cfg.files.transfer,
		DirectRedirect: cfg.router.directRedirect,
	}

	err = attachAccountSystem(ctx, cfg, &handlerCfg, store.auth, log)
	if err != nil {
		return nil, err
	}

	err = attachFiles(ctx, cfg, &handlerCfg, store.files, shared,
		&fileDeps{expired: expired, metrics: metrics.files}, log)
	if err != nil {
		return nil, err
	}

	handler, err := web.NewHandler(&handlerCfg)
	if err != nil {
		return nil, fmt.Errorf("new handler: %w", err)
	}

	return handler, nil
}

func serverAction(ctx context.Context, cmd *cli.Command) error {
	cfg, err := configFromCommand(cmd)
	if err != nil {
		return err
	}

	return serve(ctx, &cfg)
}

// serve builds the server from cfg, serves until a signal or a listener failure and then runs
// the shutdown sequence. One manager, stop, closes the listeners, the background work and the
// database in this order; serve registers them in the reverse order.
func serve(ctx context.Context, cfg *Config) error {
	log := newLogger(cfg.log.level, cfg.log.format)
	// Bound concurrent argon2id workspace before any hasher can run.
	hasher := prepareArgon2(cfg.argon2, log)

	keyring, err := loadServeKeyring(
		cfg.keyring.path, cfg.storage.dialect, cfg.storage.dsn, cfg.keyring.allowShared, log)
	if err != nil {
		return fmt.Errorf("load content encryption keys: %w", err)
	}

	db, err := openDB(ctx, cfg.storage.dialect, cfg.storage.dsn, log)
	if err != nil {
		return err
	}

	stop := newServerStop(newStopLimits(cfg.shutdownTimeout), log)
	stop.addDatabase(db)
	stop.addBackgroundPhase()

	shared := contentServices{keyring: keyring, hasher: hasher, background: stop.background}

	router, metrics, err := buildServerRouter(ctx, cfg, db, shared, log)
	if err != nil {
		return stop.closeAfterFailedStart(ctx, err)
	}

	// The listener phase is registered last, so the listeners stop first.
	stop.addListenerPhase()

	if cfg.http.metricsAddr != "" {
		startMetricsServer(ctx, cfg.http.metricsAddr, metrics.registry, stop, log)
	}

	logServerStarted(ctx, cfg, log)

	return runServer(ctx, cfg, router, stop, log)
}

// buildServerRouter applies the migrations, prepares the stored content and makes the metrics
// registry, the request handlers and the router.
func buildServerRouter(
	ctx context.Context, cfg *Config, db *bun.DB, shared contentServices, log *slog.Logger,
) (http.Handler, serverMetrics, error) {
	var zero serverMetrics

	store, err := initRepos(ctx, cfg.storage.dialect, db, log, cfg.storage.migration())
	if err != nil {
		return nil, zero, err
	}

	err = validateContentEncryption(ctx, db, shared.keyring, shared.hasher, log)
	if err != nil {
		return nil, zero, err
	}

	metrics, err := newMetricsRegistry()
	if err != nil {
		return nil, zero, fmt.Errorf("create metrics registry: %w", err)
	}

	handler, err := buildHandlers(ctx, cfg, &store, shared, metrics, log)
	if err != nil {
		return nil, zero, fmt.Errorf("build request handlers: %w", err)
	}

	router, err := buildRouter(ctx, cfg, handler, log)
	if err != nil {
		return nil, zero, err
	}

	return router, metrics, nil
}

// logServerStarted records the listener, the storage backend and the redacted DSN at startup.
func logServerStarted(ctx context.Context, cfg *Config, log *slog.Logger) {
	tlsEnabled := cfg.http.tlsCert != ""

	// The scheme reflects TLS on this listener, independently of a reverse proxy.
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	log.InfoContext(ctx, "server started",
		"addr", cfg.http.addr, "url", listenerURL(scheme, cfg.http.addr),
		"storage", cfg.storage.dialect, "dsn", redactDSN(cfg.storage.dsn),
		"tls", tlsEnabled,
	)
}

func closeServerDB(ctx context.Context, db *bun.DB, log *slog.Logger) {
	err := db.Close()
	if err != nil {
		log.ErrorContext(ctx, "close db", "err", err)
	}
}
