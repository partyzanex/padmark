// Package testenv builds the shared environment of the integration tests: the in-process web
// stack (Stack), and the PostgreSQL and S3-compatible containers that the tests of one package
// share (Postgres, S3Mock). Package servertest starts the server binary; this package does not.
//
// A shared container has a name that contains the key of the container and the process ID of
// the test binary, and testcontainers reuses the container of that name (WithReuseByName). So the
// tests of one package share one container of each kind, and two packages never share one. The
// Ryuk reaper of testcontainers stops the shared containers after the test processes end.
package testenv

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// suffixBytes is the number of random bytes in the name of a database or a bucket.
const suffixBytes = 8

// sharedName returns the container name of key for this test process. The first call with
// the name creates the container; each later call reuses it. The callers start no two containers
// with one name at the same time: each caller is a SetupSuite or a sequential test.
func sharedName(key string) string {
	clean := strings.Map(func(char rune) rune {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			return char
		default:
			return '-'
		}
	}, strings.ToLower(key))

	return "padmark-testenv-" + clean + "-" + strconv.Itoa(os.Getpid())
}

// randomSuffix returns a random lower-case hex text for the name of a new database or bucket, so
// the names of two callers do not collide.
func randomSuffix(t *testing.T) string {
	t.Helper()

	random := make([]byte, suffixBytes)

	_, err := rand.Read(random)
	require.NoError(t, err)

	return hex.EncodeToString(random)
}
