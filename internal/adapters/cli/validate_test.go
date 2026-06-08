package cli

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateBurnTTL_LoneTTLRejected verifies that --ttl without --burn is rejected with a
// clear message, for both `create` and `edit` (the contract: --ttl only with --burn).
func TestValidateBurnTTL_LoneTTLRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{cmdCreate, []string{cmdCreate, "--ttl", "60"}},
		{cmdEdit, []string{cmdEdit, "some-id", "--edit-code", "code", "--ttl", "60"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := runCLI(context.Background(), testCase.args...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "--ttl requires --burn")
		})
	}
}

// TestValidateBurnTTL_TTLWithBurnPasses verifies the validation does NOT trip when --burn is
// present: the command proceeds past validateBurnTTL and fails later for an unrelated reason
// (empty content), proving the TTL contract check passed.
func TestValidateBurnTTL_TTLWithBurnPasses(t *testing.T) {
	t.Parallel()

	err := runCLI(context.Background(), cmdCreate, "--burn", "--ttl", "60", "--content", "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--ttl requires --burn",
		"validation must pass when --burn is set; failure must come from later checks")
}

// TestIsCharDevice covers the stdin-terminal predicate used to fail fast instead of hanging on
// an interactive stdin: only a character-device mode counts as a terminal.
func TestIsCharDevice(t *testing.T) {
	t.Parallel()

	assert.True(t, isCharDevice(os.ModeCharDevice), "a char device is an interactive terminal")
	assert.True(t, isCharDevice(os.ModeCharDevice|0o600), "extra permission bits must not matter")
	assert.False(t, isCharDevice(os.ModeNamedPipe), "a pipe is not a terminal (content is piped in)")
	assert.False(t, isCharDevice(0), "a regular file is not a terminal")
}
