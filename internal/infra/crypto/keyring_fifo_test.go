//go:build darwin || linux

package crypto_test

import (
	"log/slog"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// Opening a FIFO blocks until a writer appears, so the type check must precede the open.
func TestLoadContentKeyring_RejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.fifo")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	done := make(chan error, 1)

	go func() {
		_, err := crypto.LoadContentKeyring(path, slog.New(slog.DiscardHandler), strictLoad())
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("LoadContentKeyring blocked on a FIFO")
	}
}

func TestLoadContentKeyring_RejectsDirectory(t *testing.T) {
	_, err := crypto.LoadContentKeyring(t.TempDir(), slog.New(slog.DiscardHandler), strictLoad())
	require.ErrorContains(t, err, "regular file")
}
