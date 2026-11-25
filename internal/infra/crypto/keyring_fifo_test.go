//go:build darwin || linux

package crypto_test

import (
	"log/slog"
	"path/filepath"
	"syscall"
	"time"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// Opening a FIFO blocks until a writer appears, so the type check must precede the open.
func (s *KeyringLoadSuite) TestLoadContentKeyring_RejectsFIFOWithoutBlocking() {
	path := filepath.Join(s.T().TempDir(), "keys.fifo")
	s.Require().NoError(syscall.Mkfifo(path, 0o600))

	done := make(chan error, 1)

	go func() {
		_, err := crypto.LoadContentKeyring(path, slog.New(slog.DiscardHandler), strictLoad())
		done <- err
	}()

	select {
	case err := <-done:
		s.Require().ErrorContains(err, "regular file")
	case <-time.After(5 * time.Second):
		s.T().Fatal("LoadContentKeyring blocked on a FIFO")
	}
}

func (s *KeyringLoadSuite) TestLoadContentKeyring_RejectsDirectory() {
	_, err := crypto.LoadContentKeyring(s.T().TempDir(), slog.New(slog.DiscardHandler), strictLoad())
	s.Require().ErrorContains(err, "regular file")
}
