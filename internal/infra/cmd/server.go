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

func serverAction(ctx context.Context, cmd *cli.Command) error {
	// 1. Logger
	log := newLogger(cmd.String(FlagLogLevel), cmd.String(FlagLogFormat))

	// 2. Storage
	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

	db, err := openDB(ctx, storage, dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.ErrorContext(ctx, "close db", "err", closeErr)
		}
	}()

	repo, err := initStorage(ctx, storage, db)
	if err != nil {
		return err
	}

	// 3. Renderer
	renderer := render.NewRenderer()

	// 4. Manager
	manager := notes.NewManager(repo, renderer, log)

	// 5. Handler + Router
	handler := adaptershttp.NewHandler(manager, log).WithPinger(db.DB)
	ogenHandler := adaptershttp.NewOgenHandler(manager, db.DB, log)

	var tokens []string

	if raw := cmd.String(FlagAuthTokens); raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			if tok := strings.TrimSpace(part); tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}

	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return err
	}

	routerOpts := adaptershttp.RouterOptions{
		CookieMaxAge:   cmd.Int(FlagCookieMaxAge),
		MaxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		RateLimit:      cmd.Int(FlagRateLimit),
		RateBurst:      cmd.Int(FlagRateBurst),
		TrustedProxies: trustedProxies,
	}
	router := adaptershttp.NewRouter(handler, ogenHandler, tokens, routerOpts)

	// 6. Server
	tlsCert := cmd.String(FlagTLSCert)
	tlsKey := cmd.String(FlagTLSKey)

	if (tlsCert == "") != (tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must both be set to enable TLS")
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

	go func() {
		log.InfoContext(ctx, "server started",
			"addr", srv.Addr, "storage", storage, "dsn", redactDSN(dsn), "tls", tlsCert != "",
		)

		var serveErr error
		if tlsCert != "" {
			serveErr = srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			serveErr = srv.ListenAndServe()
		}

		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			cancelServer(serveErr)
		} else {
			cancelServer(nil)
		}
	}()

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err = compat.CloseOnSignal(serverCtx, shutCtx, syscall.SIGINT, syscall.SIGTERM); err != nil { //nolint:contextcheck
		// Context was cancelled — return the server error if that's the cause.
		if cause := context.Cause(serverCtx); cause != nil {
			return cause
		}

		return nil
	}

	return nil
}
