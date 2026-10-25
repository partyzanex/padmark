package server

import (
	"context"
	"errors"
	"testing"

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

			err := waitAndShutdown(serverCtx)

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
