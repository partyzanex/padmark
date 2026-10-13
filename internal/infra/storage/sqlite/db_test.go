package sqlite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithPragmas(t *testing.T) {
	const appended = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&" +
		"_pragma=journal_mode(WAL)&_txlock=immediate"

	// Bare path: every setting appended, "?" first then "&".
	require.Equal(t, "padmark.db?"+appended, WithPragmas("padmark.db"))

	// Existing query string: everything appended with "&".
	require.Equal(t, "file:db?cache=shared&"+appended, WithPragmas("file:db?cache=shared"))

	// Per-setting idempotency: a pre-set foreign_keys is not duplicated; the others are added.
	got := WithPragmas("db?_pragma=foreign_keys(0)")
	require.Equal(t, 1, strings.Count(got, "_pragma=foreign_keys"), "foreign_keys must not be duplicated")
	require.Contains(t, got, "_pragma=busy_timeout(5000)")
	require.Contains(t, got, "_pragma=journal_mode(WAL)")
	require.Contains(t, got, "_txlock=immediate")

	// An operator who deliberately picked a different transaction mode keeps it.
	deferred := WithPragmas("db?_txlock=deferred")
	require.Equal(t, 1, strings.Count(deferred, "_txlock="), "_txlock must not be duplicated")
	require.NotContains(t, deferred, "_txlock=immediate")
}
