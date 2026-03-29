package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/urfave/cli/v3"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/render"
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
	var tokens []string

	for part := range strings.SplitSeq(raw, ",") {
		if tok := strings.TrimSpace(part); tok != "" {
			tokens = append(tokens, tok)
		}
	}

	return tokens
}

func buildRouter(
	cmd *cli.Command,
	handler *adaptershttp.Handler,
	ogenHandler *adaptershttp.OgenHandler,
) (http.Handler, error) {
	var tokens []string

	if raw := cmd.String(FlagAuthTokens); raw != "" {
		tokens = parseTokens(raw)
	}

	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return nil, err
	}

	opts := adaptershttp.RouterOptions{
		CookieMaxAge:   cmd.Int(FlagCookieMaxAge),
		MaxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		RateLimit:      cmd.Int(FlagRateLimit),
		RateBurst:      cmd.Int(FlagRateBurst),
		TrustedProxies: trustedProxies,
	}

	return adaptershttp.NewRouter(handler, ogenHandler, tokens, opts), nil
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

func runServer(ctx context.Context, cmd *cli.Command, router http.Handler) error {
	tlsCert := cmd.String(FlagTLSCert)
	tlsKey := cmd.String(FlagTLSKey)

	if (tlsCert == "") != (tlsKey == "") {
		return errors.New("--tls-cert and --tls-key must both be set to enable TLS")
	}

	srv := &http.Server{
		Addr:              cmd.String(FlagAddr),
		Handler:           router,
		ReadTimeout:       time.Duration(cmd.Int(FlagReadTimeout)) * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    cmd.Int(FlagMaxHeaderBytes),
	}

	compat.Append(shutdown.ContextFn(srv.Shutdown))

	// serverCtx is cancelled with the server error if ListenAndServe fails unexpectedly,
	// allowing CloseOnSignal to unblock and propagate the cause.
	serverCtx, cancelServer := context.WithCancelCause(ctx)
	defer cancelServer(nil)

	go listenAndServe(srv, tlsCert, tlsKey, cancelServer)

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

	repo, err := initStorage(ctx, storage, db)
	if err != nil {
		return err
	}

	manager := notes.NewManager(repo, render.NewRenderer(), log)
	handler := adaptershttp.NewHandler(manager, log).WithPinger(db.DB)
	ogenHandler := adaptershttp.NewOgenHandler(manager, db.DB, log)

	router, err := buildRouter(cmd, handler, ogenHandler)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "server started",
		"addr", cmd.String(FlagAddr), "storage", storage, "dsn", redactDSN(dsn),
		"tls", cmd.String(FlagTLSCert) != "",
	)

	return runServer(ctx, cmd, router)
}
