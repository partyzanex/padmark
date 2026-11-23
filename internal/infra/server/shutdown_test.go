package server

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// newListenerStop returns a shutdown manager with the listener phase registered, as serve has
// it before the first listener starts.
func newListenerStop(limits stopLimits) *serverStop {
	stop := newServerStop(limits, slog.New(slog.DiscardHandler))
	stop.addListenerPhase()

	return stop
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
			stop := newListenerStop(newStopLimits(DefaultShutdownTimeout))

			var closed bool

			stop.listeners.Append(shutdown.ContextFn(func(ctx context.Context) error {
				closed = true

				_, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline, "shutdown must run under a bounded deadline")
				require.NoError(t, ctx.Err(), "shutdown context must outlive the cancelled server context")

				return testCase.closeErr
			}))

			serverCtx, cancel := context.WithCancelCause(t.Context())
			cancel(errBind)

			err := waitAndShutdown(serverCtx, stop)

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
	const timeout = 3 * time.Minute

	stop := newListenerStop(newStopLimits(timeout))

	var remaining time.Duration

	stop.listeners.Append(shutdown.ContextFn(func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)

		return nil
	}))

	serverCtx, cancel := context.WithCancelCause(t.Context())
	cancel(nil)

	_ = waitAndShutdown(serverCtx, stop)

	require.Greater(t, remaining, timeout-time.Minute)
	require.LessOrEqual(t, remaining, timeout)
}

// TestShutdown_BackgroundStopsAfterListenerUsesFullTimeout reproduces R-10: a listener closer
// uses all of the time of the listener phase. The background phase has its own deadline, so the
// one manager still stops the file sweep and waits for it.
func TestShutdown_BackgroundStopsAfterListenerUsesFullTimeout(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	// The listener phase gets 50 ms, as before; the background phase gets the 1 s that the test
	// gave stopBackground when the background work had a manager of its own.
	limits := stopLimits{drain: 40 * time.Millisecond, handlers: 10 * time.Millisecond, background: time.Second}
	stop := newServerStop(limits, log)
	stop.addBackgroundPhase()
	stop.addListenerPhase()

	stop.listeners.Append(shutdown.ContextFn(func(ctx context.Context) error {
		<-ctx.Done() // A long file transfer holds srv.Shutdown until the deadline.

		return ctx.Err()
	}))

	sweepStopped := make(chan struct{})

	startFileSweep(t.Context(), time.Hour, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		close(sweepStopped)

		return 0, ctx.Err()
	}, stop.background, log)

	serverCtx, cancel := context.WithCancelCause(t.Context())
	cancel(nil)

	err := waitAndShutdown(serverCtx, stop)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case <-sweepStopped:
	default:
		t.Fatal("the shutdown sequence returned before the sweep stopped")
	}
}

// TestServerStop_ClosesListenersThenBackgroundThenDatabase makes sure that the one manager
// closes the phases in the reverse order of their registration: the listeners first, then the
// background work, and the database last.
func TestServerStop_ClosesListenersThenBackgroundThenDatabase(t *testing.T) {
	db := openTestDB(t)

	var (
		logs    syncBuffer
		orderMu sync.Mutex
		order   []string
	)

	stop := newServerStop(newStopLimits(DefaultShutdownTimeout), slog.New(slog.NewTextHandler(&logs, nil)))
	stop.addDatabase(db)
	stop.addBackgroundPhase()
	stop.addListenerPhase()

	// record runs in the goroutine of a closer, so it records and does not assert.
	record := func(name string) shutdown.ContextFn {
		return func(ctx context.Context) error {
			orderMu.Lock()
			defer orderMu.Unlock()

			state := "database open"
			if db.PingContext(ctx) != nil {
				state = "database closed"
			}

			order = append(order, name+": "+state)

			return nil
		}
	}

	stop.background.Append(record("background"))
	stop.listeners.Append(record("main listener"))
	stop.listeners.Append(record("metrics listener"))

	serverCtx, cancel := context.WithCancelCause(t.Context())
	cancel(nil)

	err := waitAndShutdown(serverCtx, stop)

	require.ErrorIs(t, err, context.Canceled, "the server context ended; the stop itself has no error")
	require.NotContains(t, err.Error(), "graceful shutdown")
	require.Len(t, order, 3)
	require.ElementsMatch(t, []string{"main listener: database open", "metrics listener: database open"}, order[:2])
	require.Equal(t, []string{"background: database open"}, order[2:], "the background work stops after the listeners")
	require.ErrorContains(t, db.PingContext(t.Context()), "database is closed", "the database closes last")
	requireLogOrder(t, logs.String(),
		"shutdown started: the server context ended",
		"shutdown: listeners stop started", "shutdown: listeners stopped",
		"shutdown: background work stop started", "shutdown: background work stopped",
		"shutdown: database close started", "shutdown: database closed")
}

// TestServerStop_AfterFailedStartClosesBackgroundAndDatabase covers the startup failure: only
// the database and the background work are registered, and a cancelled context does not skip
// their close.
func TestServerStop_AfterFailedStartClosesBackgroundAndDatabase(t *testing.T) {
	errStart := errors.New("validate content encryption: key does not match")
	db := openTestDB(t)

	var logs syncBuffer

	stop := newServerStop(newStopLimits(DefaultShutdownTimeout), slog.New(slog.NewTextHandler(&logs, nil)))
	stop.addDatabase(db)
	stop.addBackgroundPhase()

	backgroundStopped := false

	stop.background.Append(shutdown.Fn(func() error {
		backgroundStopped = true

		return nil
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := stop.closeAfterFailedStart(ctx, errStart)

	require.ErrorIs(t, err, errStart)
	require.Equal(t, errStart.Error(), err.Error(), "the close adds no text to the startup error")
	require.True(t, backgroundStopped)
	require.ErrorContains(t, db.PingContext(t.Context()), "database is closed")
	require.NotContains(t, logs.String(), "listeners stop")
	requireLogOrder(t, logs.String(), "shutdown: background work stopped", "shutdown: database closed")
}

// --- helpers ---

// openTestDB opens a SQLite file database as serve does. The shutdown sequence closes it.
func openTestDB(t *testing.T) *bun.DB {
	t.Helper()

	db, err := openDB(t.Context(), storageSQLite, filepath.Join(t.TempDir(), "padmark.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	return db
}

// requireLogOrder makes sure that each message is in logs, after the message before it.
func requireLogOrder(t *testing.T, logs string, messages ...string) {
	t.Helper()

	position := 0

	for _, message := range messages {
		index := strings.Index(logs[position:], message)
		require.NotEqual(t, -1, index, "%q must follow the earlier records in:\n%s", message, logs)

		position += index + len(message)
	}
}
