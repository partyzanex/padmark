package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateTTL_LoneTTLAccepted verifies that --ttl without --burn passes validation for both
// `create` and `edit`: it is a lifetime there. The commands fail later for unrelated reasons.
func TestValidateTTL_LoneTTLAccepted(t *testing.T) {
	isolateEnv(t)

	cases := []struct {
		name string
		args []string
	}{
		{cmdCreate, []string{cmdCreate, "--ttl", "60", "--content", ""}},
		{cmdEdit, []string{cmdEdit, "some-id", "--edit-code", "code", "--ttl", "60", "--content", ""}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := runCLI(t.Context(), testCase.args...)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), "TTL")
		})
	}
}

// TestValidateTTL_TTLWithBurnPasses verifies the validation does NOT trip when --burn is
// present: the command proceeds past validateTTL and fails later for an unrelated reason
// (empty content), proving the TTL contract check passed.
func TestValidateTTL_TTLWithBurnPasses(t *testing.T) {
	isolateEnv(t)

	err := runCLI(t.Context(), cmdCreate, "--burn", "--ttl", "60", "--content", "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "TTL",
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
