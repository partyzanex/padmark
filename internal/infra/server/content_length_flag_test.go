package server

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/domain"
)

// parseMaxContentLength runs the real flag set, with the environment of the test, and returns
// the note content limit that the server uses.
func parseMaxContentLength(t *testing.T, args ...string) int {
	t.Helper()

	var got int

	cmd := clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Flags = appFlags()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			cfg, err := configFromCommand(cmd)
			got = cfg.maxContentLength

			return err
		}
	})

	require.NoError(t, cmd.Run(t.Context(), append([]string{"padmark"}, args...)))

	return got
}

func TestMaxContentLength_FollowsBodyLimitByDefault(t *testing.T) {
	require.Equal(t, DefaultMaxBodyBytes, parseMaxContentLength(t))
	require.Equal(t, 8<<20, parseMaxContentLength(t, "--max-body-bytes", "8388608"))
}

func TestMaxContentLength_FromEnvironment(t *testing.T) {
	t.Setenv("PADMARK_MAX_BODY_BYTES", "16777216")
	require.Equal(t, 16<<20, parseMaxContentLength(t), "the body limit alone raises the content limit")

	t.Setenv("PADMARK_MAX_CONTENT_LENGTH", "2000000")
	require.Equal(t, 2000000, parseMaxContentLength(t))
}

func TestMaxContentLength_Flag(t *testing.T) {
	require.Equal(t, 100, parseMaxContentLength(t, "--max-content-length", "100"))
}

func TestMaxContentLength_StorageLimit(t *testing.T) {
	require.Equal(t, domain.MaxContentBytes,
		parseMaxContentLength(t, "--max-content-length", strconv.Itoa(domain.MaxContentBytes)))

	err := NewApp().Run(t.Context(), []string{
		"padmark", "serve", "--max-content-length", strconv.Itoa(domain.MaxContentBytes + 1),
	})
	// urfave/cli formats a validator error with %v, so only the text reaches the caller.
	require.ErrorContains(t, err, errMaxContentLengthTooLarge.Error())
	require.ErrorContains(t, err, strconv.Itoa(domain.MaxContentBytes), "the message gives the limit")
}

func TestMaxContentLength_RejectsNegative(t *testing.T) {
	err := NewApp().Run(t.Context(), []string{"padmark", "serve", "--max-content-length", "-1"})
	require.ErrorContains(t, err, "max content length must not be negative")
}
