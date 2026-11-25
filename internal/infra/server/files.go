package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/partyzanex/shutdown"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// s3PingTimeout limits the bucket check and the bucket creation at startup.
const s3PingTimeout = 10 * time.Second

// errInvalidSweepInterval rejects a negative --files-sweep-interval. It stays next to the flag
// user, because only the file wiring reads this flag.
var errInvalidSweepInterval = errors.New("files sweep interval must not be negative; use 0 to turn it off")

// fileDeps holds the collaborators that the file manager shares with the note and link managers,
// and the file metrics. expired gets the file manager, so that an expired file that the sweep
// did not remove yet does not block a new note, link or file with its slug.
type fileDeps struct {
	expired *expiredslugs.Releaser
	metrics files.Metrics
}

// attachFiles turns on file publishing when --s3-endpoint is set: it puts the file manager into
// handlerCfg.Files. Without the endpoint the field stays nil. configFromCommand already checked
// that the bucket and the keys are set. An unusable configuration stops startup (fail-hard): an
// operator who configures files expects them to work, and a wrong bucket or key is a
// configuration error, not a temporary failure. The file sweep registers its close function in
// shared.background, which stops before the database closes.
func attachFiles(
	ctx context.Context, cfg *Config, handlerCfg *web.HandlerConfig, storage files.Storage,
	shared contentServices, deps *fileDeps, log *slog.Logger,
) error {
	if cfg.files.s3.Endpoint == "" {
		logFilesOff(ctx, cfg.files.orphans, log)

		return nil
	}

	objects, err := openObjectStore(ctx, &cfg.files, log)
	if err != nil {
		return err
	}

	encryptor, err := shared.keyring.ForResource("file")
	if err != nil {
		return fmt.Errorf("file content cipher: %w", err)
	}

	manager := files.NewManager(&files.Deps{
		Storage:      storage,
		Objects:      objects,
		Cipher:       crypto.NewFileCipher(),
		Encryptor:    encryptor,
		Hasher:       shared.hasher,
		ExpiredSlugs: deps.expired,
		Log:          log,
		Now:          time.Now,
	}, cfg.files.maxFileSize, cfg.customSlugs).
		WithMetrics(deps.metrics)

	// Composition time, before the first request: the shared releaser now also removes files.
	deps.expired.WithFiles(manager)
	handlerCfg.Files = manager
	log.InfoContext(ctx, "file publishing is on", "bucket", cfg.files.s3.Bucket, "max_file_size", manager.MaxSize())

	startFileSweep(ctx, cfg.files.sweepInterval, manager.SweepExpired, shared.background, log)

	return nil
}

// openObjectStore connects to the storage and makes sure that the bucket exists. It creates a
// missing bucket, so that the operator does not have to make it by hand before the first start.
func openObjectStore(ctx context.Context, cfg *filesConfig, log *slog.Logger) (*s3store.Store, error) {
	var storeOpts []s3store.Option
	if cfg.allowInsecure {
		storeOpts = append(storeOpts, s3store.AllowInsecureHTTP())
	}

	objects, err := s3store.New(&cfg.s3, log, storeOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3 store: %w", err)
	}

	bucketCtx, cancel := context.WithTimeout(ctx, s3PingTimeout)
	defer cancel()

	created, err := objects.EnsureBucket(bucketCtx)
	if err != nil {
		return nil, fmt.Errorf("s3 store: %w", err)
	}

	if created {
		log.InfoContext(ctx, "s3 bucket created", "bucket", cfg.s3.Bucket)
	}

	return objects, nil
}

// logFilesOff records that file publishing is off. Other S3 settings without --s3-endpoint
// usually mean a misspelled endpoint variable, so the server then logs a warning with the
// names of those settings (orphans). The values stay out of the log.
func logFilesOff(ctx context.Context, orphans []string, log *slog.Logger) {
	if len(orphans) > 0 {
		log.WarnContext(ctx, "file publishing is off: other s3 settings are set without --"+FlagS3Endpoint,
			"settings", orphans)

		return
	}

	log.InfoContext(ctx, "file publishing is off", "reason", "--"+FlagS3Endpoint+" is empty")
}

// sweepFunc removes the expired files and returns their number. files.Manager.SweepExpired
// is the production value; a function value lets the loop test work without a mock.
type sweepFunc func(ctx context.Context) (int, error)

// startFileSweep starts the background sweep of expired files. An interval of 0 turns it off.
// The goroutine sweeps once at once, so that files that expired while the server was stopped
// go without a wait of one full interval. The close function in background cancels the loop
// and waits for the current sweep until the context of the close call ends.
func startFileSweep(
	ctx context.Context, interval time.Duration, sweep sweepFunc, background *shutdown.Lifo, log *slog.Logger,
) {
	if interval <= 0 {
		log.InfoContext(ctx, "background file sweep is off")

		return
	}

	// The loop has its own cancel, because the close function must stop it before the
	// database closes. The request context values stay for the log records.
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ticker := time.NewTicker(interval)
	done := make(chan struct{})

	go func() {
		defer close(done)

		if sweepOnce(loopCtx, sweep, log) {
			runSweepLoop(loopCtx, ticker.C, sweep, log)
		}
	}()

	background.Append(shutdown.ContextFn(func(shutCtx context.Context) error {
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

// runSweepLoop calls sweep at each tick until ctx is canceled.
func runSweepLoop(ctx context.Context, ticks <-chan time.Time, sweep sweepFunc, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if !sweepOnce(ctx, sweep, log) {
				return
			}
		}
	}
}

// sweepOnce calls sweep one time. It returns false when shutdown canceled ctx. A sweep error is
// fail-soft: the next tick tries again, and each upload also sweeps. A panic is logged at the
// error level and the loop continues, because a panic must not stop the server or the sweep.
func sweepOnce(ctx context.Context, sweep sweepFunc, log *slog.Logger) (keepGoing bool) {
	defer func() {
		if rec := recover(); rec != nil {
			log.ErrorContext(ctx, "background file sweep panicked", "panic", rec, "stack", string(debug.Stack()))

			keepGoing = ctx.Err() == nil
		}
	}()

	removed, err := sweep(ctx)
	if err != nil && ctx.Err() != nil {
		// Shutdown canceled the sweep; this is not a failure.
		return false
	}

	if err != nil {
		log.WarnContext(ctx, "background file sweep failed", "err", err)

		return true
	}

	if removed > 0 {
		log.InfoContext(ctx, "expired files removed", "count", removed)
	}

	return ctx.Err() == nil
}
