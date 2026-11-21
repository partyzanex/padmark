package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/stretchr/testify/require"
)

// useIsolatedShutdownManager swaps the process-wide compat manager for a fresh one for the
// duration of the test, so closers registered here never leak into other tests.
func useIsolatedShutdownManager(t *testing.T) {
	t.Helper()

	compat.Set(shutdown.NewLIFO())
	t.Cleanup(func() { compat.Set(shutdown.NewLIFO()) })
}

func TestWaitAndShutdown_ListenerFailureClosesRegisteredResources(t *testing.T) {
	errBind := errors.New("listen tcp :443: bind: address already in use")
	errClose := errors.New("metrics listener close failed")

	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "closers succeed", closeErr: nil},
		{name: "closer fails", closeErr: errClose},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			useIsolatedShutdownManager(t)

			var closed bool

			compat.Append(shutdown.ContextFn(func(ctx context.Context) error {
				closed = true

				_, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline, "shutdown must run under a bounded deadline")
				require.NoError(t, ctx.Err(), "shutdown context must outlive the cancelled server context")

				return testCase.closeErr
			}))

			serverCtx, cancel := context.WithCancelCause(t.Context())
			cancel(errBind)

			err := waitAndShutdown(serverCtx, DefaultShutdownTimeout)

			require.True(t, closed, "listener failure must still close every registered resource")
			require.ErrorIs(t, err, errBind)
			require.ErrorContains(t, err, "server: ")

			if testCase.closeErr == nil {
				require.NotContains(t, err.Error(), "graceful shutdown")

				return
			}

			require.ErrorIs(t, err, errClose)
			require.ErrorContains(t, err, "graceful shutdown: ")
		})
	}
}

func TestWaitAndShutdown_UsesTheTimeoutArgument(t *testing.T) {
	useIsolatedShutdownManager(t)

	const timeout = 3 * time.Minute

	var remaining time.Duration

	compat.Append(shutdown.ContextFn(func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)

		return nil
	}))

	serverCtx, cancel := context.WithCancelCause(t.Context())
	cancel(nil)

	_ = waitAndShutdown(serverCtx, timeout)

	require.Greater(t, remaining, timeout-time.Minute)
	require.LessOrEqual(t, remaining, timeout)
}

// TestShutdown_BackgroundStopsAfterListenerUsesFullTimeout reproduces R-10: a listener closer
// uses all of the shutdown time, and the shared manager then skips its other closers. The file
// sweep is in the background manager, so stopBackground still stops it and waits for it.
func TestShutdown_BackgroundStopsAfterListenerUsesFullTimeout(t *testing.T) {
	useIsolatedShutdownManager(t)

	compat.Append(shutdown.ContextFn(func(ctx context.Context) error {
		<-ctx.Done() // A long file transfer holds srv.Shutdown until the deadline.

		return ctx.Err()
	}))

	background := shutdown.NewLIFO()
	sweepStopped := make(chan struct{})

	startFileSweep(t.Context(), time.Hour, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		close(sweepStopped)

		return 0, ctx.Err()
	}, background, slog.New(slog.DiscardHandler))

	serverCtx, cancel := context.WithCancelCause(t.Context())
	cancel(nil)

	err := waitAndShutdown(serverCtx, 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case <-sweepStopped:
		t.Fatal("the shared manager must not stop the sweep; the background manager does")
	default:
	}

	stopBackground(t.Context(), background, time.Second, slog.New(slog.DiscardHandler))

	select {
	case <-sweepStopped:
	default:
		t.Fatal("stopBackground returned before the sweep stopped")
	}
}
