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
	"github.com/urfave/cli/v3"

	"github.com/uptrace/bun"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 5 * time.Second
)

func serveCommand() *cli.Command {
	return &cli.Command{
		Name:   "serve",
		Usage:  "Start the HTTP server",
		Flags:  appFlags(),
		Action: serverAction,
	}
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
	cmd *cli.Command,
	handler *adaptershttp.Handler,
	ogenHandler *adaptershttp.OgenHandler,
) (http.Handler, error) {
	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return nil, err
	}

	publicScheme, err := parsePublicScheme(cmd.String(FlagPublicScheme))
	if err != nil {
		return nil, err
	}

	opts := adaptershttp.RouterOptions{
		CookieMaxAge:   cmd.Int(FlagCookieMaxAge),
		MaxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		RateLimit:      cmd.Int(FlagRateLimit),
		RateBurst:      cmd.Int(FlagRateBurst),
		TrustedProxies: trustedProxies,
		ForcedScheme:   publicScheme,
	}

	return adaptershttp.NewRouter(handler, ogenHandler, &opts), nil
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
	srv := &http.Server{
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

	srv := &http.Server{
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
// middleware passes everything through unless --auth-tokens is set).
func attachAccountSystem(
	ctx context.Context, cmd *cli.Command, handler *adaptershttp.Handler,
	users auth.UserStore, invites auth.InviteStore, sessions auth.SessionStore,
	apiTokens auth.APITokenStore, log *slog.Logger,
) *adaptershttp.Handler {
	if !cmd.Bool(FlagEnableAccounts) {
		log.InfoContext(ctx, "account system disabled (public mode); set --enable-accounts to enable login/admin")

		return handler
	}

	sessionTTL := time.Duration(cmd.Int(FlagSessionTTL)) * time.Second
	authMgr := auth.NewManager(
		users, invites, sessions, apiTokens, crypto.New(),
		crypto.NewPasswordHasher(argon2ParamsFromFlags(cmd)),
		crypto.NewKDF(), crypto.NewTOTP(),
		log, cmd.String(FlagTOTPIssuer), sessionTTL,
	)

	empty, emptyErr := authMgr.IsEmpty(ctx)
	if emptyErr != nil {
		log.WarnContext(ctx, "check users empty", "err", emptyErr)
	} else if empty {
		log.InfoContext(ctx, "No users found. Open /setup to create the first admin.")
	}

	return handler.WithAuthManager(authMgr)
}

//nolint:ireturn // factory: returns different concrete impls (postgres vs sqlite) behind common interfaces
func buildAuthRepos(
	storage string, db *bun.DB,
) (auth.UserStore, auth.InviteStore, auth.SessionStore, auth.APITokenStore, adaptershttp.RevealTokenStore) {
	switch storage {
	case storagePostgres:
		return postgres.NewUserRepository(db),
			postgres.NewInviteRepository(db),
			postgres.NewSessionRepository(db),
			postgres.NewAPITokenRepository(db),
			postgres.NewRevealRepository(db)
	default:
		return sqlite.NewUserRepository(db),
			sqlite.NewInviteRepository(db),
			sqlite.NewSessionRepository(db),
			sqlite.NewAPITokenRepository(db),
			sqlite.NewRevealRepository(db)
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

	repo, err := initStorage(ctx, storage, db, log)
	if err != nil {
		return err
	}

	authTokens := parseTokens(cmd.String(FlagAuthTokens))
	manager := notes.NewManager(repo, render.NewRenderer(), crypto.New(),
		crypto.NewEditCodeHasher(), log)

	userRepo, inviteRepo, sessionRepo, apiTokenRepo, revealStore := buildAuthRepos(storage, db)

	handler := adaptershttp.NewHandler(manager, log, authTokens).
		WithRevealStore(revealStore)
	handler = attachAccountSystem(ctx, cmd, handler, userRepo, inviteRepo, sessionRepo, apiTokenRepo, log)

	ogenHandler := adaptershttp.NewOgenHandler(manager, db.DB, log)

	router, err := buildRouter(cmd, handler, ogenHandler)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "server started",
		"addr", cmd.String(FlagAddr), "storage", storage, "dsn", redactDSN(dsn),
		"tls", cmd.String(FlagTLSCert) != "",
	)

	return runServer(ctx, cmd, router, log)
}
