package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// s3PingTimeout limits the bucket check at startup.
const s3PingTimeout = 10 * time.Second

// errInvalidSweepInterval rejects a negative --files-sweep-interval. It stays next to the flag
// user, because only the file wiring reads this flag.
var errInvalidSweepInterval = errors.New("files sweep interval must not be negative; use 0 to turn it off")

// fileDeps holds the collaborators that the file manager shares with the note and link managers,
// and the file metrics.
type fileDeps struct {
	slugLock files.SlugLock
	notes    files.SlugChecker
	links    files.SlugChecker
	hasher   files.EditCodeHasher
	metrics  files.Metrics
}

// attachFiles turns on file publishing when --s3-endpoint is set. A set but unusable
// configuration stops startup (fail-hard): an operator who configures files expects them to
// work, and a wrong bucket or key is a configuration error, not a temporary failure.
func attachFiles(
	ctx context.Context, cmd *cli.Command, handler *web.Handler, store serverStorage,
	keyring *crypto.ContentKeyring, deps *fileDeps, log *slog.Logger,
) error {
	endpoint := cmd.String(FlagS3Endpoint)
	if endpoint == "" {
		return nil
	}

	cfg := s3store.Config{
		Endpoint:  endpoint,
		Bucket:    cmd.String(FlagS3Bucket),
		Region:    cmd.String(FlagS3Region),
		AccessKey: os.Getenv(EnvS3AccessKey),
		SecretKey: os.Getenv(EnvS3SecretKey),
		PathStyle: cmd.Bool(FlagS3PathStyle),
	}

	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return fmt.Errorf("%w: --%s, %s and %s are required with --%s",
			errS3Config, FlagS3Bucket, EnvS3AccessKey, EnvS3SecretKey, FlagS3Endpoint)
	}

	objects, err := s3store.New(&cfg, log)
	if err != nil {
		return fmt.Errorf("s3 store: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, s3PingTimeout)
	defer cancel()

	err = objects.Ping(pingCtx)
	if err != nil {
		return fmt.Errorf("s3 store: %w", err)
	}

	encryptor, err := keyring.ForResource("file")
	if err != nil {
		return fmt.Errorf("file content cipher: %w", err)
	}

	manager := files.NewManager(&files.Deps{
		Storage:   store.files,
		Objects:   objects,
		Cipher:    crypto.NewFileCipher(),
		Encryptor: encryptor,
		Hasher:    deps.hasher,
		SlugLock:  deps.slugLock,
		Notes:     deps.notes,
		Links:     deps.links,
		Log:       log,
	}, cmd.Int64(FlagMaxFileSize), cmd.Bool(FlagCustomSlugs)).
		WithMetrics(deps.metrics)

	handler.WithFileManager(manager)
	log.InfoContext(ctx, "file publishing is on", "bucket", cfg.Bucket, "max_file_size", manager.MaxSize())

	startFileSweep(ctx, cmd.Duration(FlagFilesSweepInterval), manager.SweepExpired, log)

	return nil
}

// sweepFunc removes the expired files and returns their number. files.Manager.SweepExpired
// is the production value; a function value lets the loop test work without a mock.
type sweepFunc func(ctx context.Context) (int, error)

// startFileSweep starts the background sweep of expired files. An interval of 0 turns it off.
// The shared shutdown manager stops the loop and waits for the current sweep to end.
func startFileSweep(ctx context.Context, interval time.Duration, sweep sweepFunc, log *slog.Logger) {
	if interval <= 0 {
		log.InfoContext(ctx, "background file sweep is off")

		return
	}

	// The loop has its own cancel, because the shutdown manager must stop it before the
	// database closes. The request context values stay for the log records.
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ticker := time.NewTicker(interval)
	done := make(chan struct{})

	go func() {
		defer close(done)

		runSweepLoop(loopCtx, ticker.C, sweep, log)
	}()

	compat.Append(shutdown.ContextFn(func(shutCtx context.Context) error {
		cancel()
		ticker.Stop()

		select {
		case <-done:
			return nil
		case <-shutCtx.Done():
			return fmt.Errorf("stop file sweep: %w", shutCtx.Err())
		}
	}))
}

// runSweepLoop calls sweep at each tick until ctx is canceled. A sweep error is fail-soft: the
// loop logs it and tries again at the next tick, and each upload also sweeps.
func runSweepLoop(ctx context.Context, ticks <-chan time.Time, sweep sweepFunc, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			removed, err := sweep(ctx)
			if err != nil && ctx.Err() != nil {
				// Shutdown canceled the sweep; this is not a failure.
				return
			}

			if err != nil {
				log.WarnContext(ctx, "background file sweep failed", "err", err)

				continue
			}

			if removed > 0 {
				log.InfoContext(ctx, "expired files removed", "count", removed)
			}
		}
	}
}
