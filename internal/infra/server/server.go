package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"

	"github.com/uptrace/bun"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/linkmetrics"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 5 * time.Second
	// minCSRFSecretLen is the minimum accepted length for --csrf-secret; it matches the
	// 32-byte secret NewRouter generates when the flag is empty.
	minCSRFSecretLen = 32
)

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
		return nil, fmt.Errorf("csrf secret must be at least %d characters, got %d", minCSRFSecretLen, len(raw))
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

func buildRouter(
	ctx context.Context,
	cmd *cli.Command,
	handler *adaptershttp.Handler,
	ogenHandler *adaptershttp.OgenHandler,
	log *slog.Logger,
) (http.Handler, error) {
	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return nil, err
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
		return nil, err
	}

	csrfSecret, err := parseCSRFSecret(cmd.String(FlagCSRFSecret))
	if err != nil {
		return nil, err
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

	opts := adaptershttp.RouterOptions{
		MaxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		RateLimit:      cmd.Int(FlagRateLimit),
		RateBurst:      cmd.Int(FlagRateBurst),
		TrustedProxies: trustedProxies,
		ForcedScheme:   publicScheme,
		// Same flag attachAccountSystem feeds into auth.NewManager's sessionTTL, so the TOTP
		// session cookie's Max-Age always matches the actual server-side session lifetime.
		SessionTTL:     time.Duration(cmd.Int(FlagSessionTTL)) * time.Second,
		DisableAPI:     cmd.Bool(FlagDisableAPI),
		DirectRedirect: cmd.Bool(FlagDirectRedirect),
		// Nil (flag empty) lets NewRouter generate a fresh CSRF secret instead of accepting
		// an injected one — the legacy single-instance behaviour.
		CSRFSecret: csrfSecret,
	}

	router, err := adaptershttp.NewRouter(handler, ogenHandler, &opts)
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
func startRedirectServer(ctx context.Context, addr string, allowedHosts map[string]struct{}, log *slog.Logger) {
	srv := &http.Server{ //nolint:exhaustruct_v5 // unset stdlib fields stay at zero = stdlib defaults apply
		Addr:              addr,
		Handler:           httpRedirectHandler(allowedHosts),
		ReadHeaderTimeout: readHeaderTimeout,
	}

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
func startMetricsServer(ctx context.Context, addr string, reg *prometheus.Registry, log *slog.Logger) {
	mux := http.NewServeMux()

	metricsOpts := promhttp.HandlerOpts{} //nolint:exhaustruct_v5 // promhttp defaults apply
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, metricsOpts))

	srv := &http.Server{ //nolint:exhaustruct_v5 // unset stdlib fields stay at zero = stdlib defaults apply
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

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

func runServer(ctx context.Context, cmd *cli.Command, router http.Handler, log *slog.Logger) error {
	tlsCert := cmd.String(FlagTLSCert)
	tlsKey := cmd.String(FlagTLSKey)

	if (tlsCert == "") != (tlsKey == "") {
		return errors.New("--tls-cert and --tls-key must both be set to enable TLS")
	}

	srv := &http.Server{ //nolint:exhaustruct_v5 // unset stdlib fields stay at zero = stdlib defaults apply
		Addr:              cmd.String(FlagAddr),
		Handler:           router,
		ReadTimeout:       time.Duration(cmd.Int(FlagReadTimeout)) * time.Second,
		WriteTimeout:      time.Duration(cmd.Int(FlagWriteTimeout)) * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    cmd.Int(FlagMaxHeaderBytes),
	}

	compat.Append(shutdown.ContextFn(srv.Shutdown))

	// serverCtx is cancelled with the server error if ListenAndServe fails unexpectedly,
	// allowing CloseOnSignal to unblock and propagate the cause.
	serverCtx, cancelServer := context.WithCancelCause(ctx)
	defer cancelServer(nil)

	go listenAndServe(srv, tlsCert, tlsKey, cancelServer)

	if tlsCert != "" {
		if redirectAddr := cmd.String(FlagHTTPRedirectAddr); redirectAddr != "" {
			startRedirectServer(ctx, redirectAddr, allowedHostSet(cmd.String(FlagAllowedHosts)), log)
		}
	}

	// context.Background() is intentional: shutCtx must outlive serverCtx so that
	// graceful shutdown can complete even when serverCtx is already cancelled.
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err := compat.CloseOnSignal(serverCtx, shutCtx, syscall.SIGINT, syscall.SIGTERM)
	if err != nil {
		cause := context.Cause(serverCtx)
		if cause != nil {
			return fmt.Errorf("server: %w", cause)
		}
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
	ctx context.Context, cmd *cli.Command, handler *adaptershttp.Handler,
	users auth.UserStore, invites auth.InviteStore, sessions auth.SessionStore,
	apiTokens auth.APITokenStore, log *slog.Logger,
) (*adaptershttp.Handler, error) {
	if !cmd.Bool(FlagEnableAccounts) {
		log.InfoContext(ctx, "account system disabled (public mode); set --enable-accounts to enable login/admin")

		return handler, nil
	}

	sessionTTL := time.Duration(cmd.Int(FlagSessionTTL)) * time.Second

	authMgr, err := auth.NewManager(
		users, invites, sessions, apiTokens,
		crypto.NewPasswordHasher(argon2ParamsFromFlags(cmd)),
		crypto.NewSealer(argon2ParamsFromFlags(cmd)), crypto.NewTOTP(),
		log, cmd.String(FlagTOTPIssuer), sessionTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("new auth manager: %w", err)
	}

	empty, emptyErr := authMgr.IsEmpty(ctx)
	if emptyErr != nil {
		log.WarnContext(ctx, "check users empty", "err", emptyErr)
	} else if empty {
		log.InfoContext(ctx, "No users found. Open /setup to create the first admin.")
	}

	return handler.WithAuthManager(authMgr), nil
}

//nolint:ireturn // factory: returns different concrete impls (postgres vs sqlite) behind common interfaces
func buildAuthRepos(
	storage string, db *bun.DB,
) (auth.UserStore, auth.InviteStore, auth.SessionStore, auth.APITokenStore, adaptershttp.RevealTokenStore) {
	switch storage {
	case storagePostgres:
		return postgres.NewUserRepository(db),
			postgres.NewInviteRepository(db, slog.Default()),
			postgres.NewSessionRepository(db, slog.Default()),
			postgres.NewAPITokenRepository(db),
			postgres.NewRevealRepository(db, slog.Default())
	default:
		return sqlite.NewUserRepository(db),
			sqlite.NewInviteRepository(db, slog.Default()),
			sqlite.NewSessionRepository(db, slog.Default()),
			sqlite.NewAPITokenRepository(db),
			sqlite.NewRevealRepository(db, slog.Default())
	}
}

// argon2ParamsFromFlags reads the configurable argon2id cost from flags/env.
// Zero values fall back to the built-in defaults inside the crypto constructors.
func argon2ParamsFromFlags(cmd *cli.Command) crypto.Argon2Params {
	return crypto.Argon2Params{
		Memory:  uint32(cmd.Int(FlagArgon2Memory)), //nolint:gosec // G115: operator-set small cost value
		Time:    uint32(cmd.Int(FlagArgon2Time)),   //nolint:gosec // G115: operator-set small cost value
		Threads: uint8(cmd.Int(FlagArgon2Threads)), //nolint:gosec // G115: operator-set small cost value
	}
}

// newMetricsRegistry builds the Prometheus registry backing the optional /metrics listener,
// pre-registered with the standard Go/process collectors plus the links.Metrics counters.
func newMetricsRegistry() (*prometheus.Registry, *linkmetrics.Metrics, error) {
	reg := prometheus.NewRegistry()

	procOpts := collectors.ProcessCollectorOpts{} //nolint:exhaustruct_v5 // collector defaults apply
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

// buildHandlers assembles the notes/links business-logic managers and wires them into the
// JSON/HTML handler and the ogen-generated handler, including the optional account system and
// link cache/metrics collaborators.
func buildHandlers(
	ctx context.Context, cmd *cli.Command, storage string, db *bun.DB,
	repo notes.Storage, linkRepo links.Storage, log *slog.Logger, linkMetrics *linkmetrics.Metrics,
) (*adaptershttp.Handler, *adaptershttp.OgenHandler, error) {
	var checker links.NotesChecker

	switch storage {
	case storagePostgres:
		checker = newPostgresNotesChecker(db)
	default:
		checker = newSQLiteNotesChecker(db)
	}

	manager := notes.NewManager(repo, linkRepo, render.NewRenderer(), crypto.New(),
		crypto.NewEditCodeHasher(), log, cmd.Bool(FlagCustomSlugs))
	linkManager := links.NewManager(linkRepo, checker, crypto.New(),
		crypto.NewEditCodeHasher(), log, cmd.Bool(FlagCustomSlugs)).
		WithCache(linkcache.NewDefault(log)).
		WithMetrics(linkMetrics)

	userRepo, inviteRepo, sessionRepo, apiTokenRepo, revealStore := buildAuthRepos(storage, db)

	handler := adaptershttp.NewHandler(manager, linkManager, log).
		WithRevealStore(revealStore)

	handler, err := attachAccountSystem(ctx, cmd, handler, userRepo, inviteRepo, sessionRepo, apiTokenRepo, log)
	if err != nil {
		return nil, nil, err
	}

	ogenHandler := adaptershttp.NewOgenHandler(manager, linkManager, db.DB, log)

	return handler, ogenHandler, nil
}

// initRepos runs migrations and builds the note and link repositories for the chosen backend.
// openDB already validated the backend name, so unknown values cannot reach the default branch.
//
//nolint:ireturn // dispatches by backend behind the notes/links usecase Storage interfaces
func initRepos(
	ctx context.Context, storage string, db *bun.DB, log *slog.Logger,
) (repo notes.Storage, linkRepo links.Storage, err error) {
	switch storage {
	case storagePostgres:
		repo, err = initPostgresStorage(ctx, db, log)
		if err != nil {
			return nil, nil, err
		}

		linkRepo = newPostgresLinkRepository(db)
	default:
		repo, err = initSQLiteStorage(ctx, db, log)
		if err != nil {
			return nil, nil, err
		}

		linkRepo = newSQLiteLinkRepository(db)
	}

	return repo, linkRepo, nil
}

func serverAction(ctx context.Context, cmd *cli.Command) error {
	log := newLogger(cmd.String(FlagLogLevel), cmd.String(FlagLogFormat))

	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

	db, err := openDB(ctx, storage, dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	defer func() {
		closeErr := db.Close()
		if closeErr != nil {
			log.ErrorContext(ctx, "close db", "err", closeErr)
		}
	}()

	repo, linkRepo, err := initRepos(ctx, storage, db, log)
	if err != nil {
		return err
	}

	metricsRegistry, linkMetrics, err := newMetricsRegistry()
	if err != nil {
		return err
	}

	handler, ogenHandler, err := buildHandlers(ctx, cmd, storage, db, repo, linkRepo, log, linkMetrics)
	if err != nil {
		return err
	}

	router, err := buildRouter(ctx, cmd, handler, ogenHandler, log)
	if err != nil {
		return err
	}

	if metricsAddr := cmd.String(FlagMetricsAddr); metricsAddr != "" {
		startMetricsServer(ctx, metricsAddr, metricsRegistry, log)
	}

	log.InfoContext(ctx, "server started",
		"addr", cmd.String(FlagAddr), "storage", storage, "dsn", redactDSN(dsn),
		"tls", cmd.String(FlagTLSCert) != "",
	)

	return runServer(ctx, cmd, router, log)
}
