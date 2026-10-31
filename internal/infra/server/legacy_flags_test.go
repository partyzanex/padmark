package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// runWithSessionTTL parses args against the real flag set and returns what sessionTTL resolves
// together with the log output of warnDeprecatedSessionFlags.
func runWithSessionTTL(t *testing.T, args ...string) (time.Duration, string) {
	t.Helper()

	var (
		got time.Duration
		out syncBuffer
	)

	log := slog.New(slog.NewTextHandler(&out, nil))
	cmd := newCommand("padmark", "test", appFlags(), func(ctx context.Context, cmd *cli.Command) error {
		warnDeprecatedSessionFlags(ctx, cmd, log)
		got = sessionTTL(cmd)

		return nil
	})

	require.NoError(t, cmd.Run(t.Context(), append([]string{"padmark"}, args...)))

	return got, out.String()
}

func TestServerRejectsRemovedAuthTokens(t *testing.T) {
	const secret = "s3cr3t-legacy-token-value"

	tests := []struct {
		name string
		env  bool
		args []string
	}{
		{name: "environment variable", env: true,
			args: nil,
		},
		{name: "command line flag", args: []string{"--auth-tokens", secret},
			env: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.env {
				t.Setenv(EnvAuthTokens, secret)
			}

			dir := t.TempDir()
			dsn := filepath.Join(dir, "padmark.db")
			args := append([]string{"padmark", "serve", "--dsn", dsn}, testCase.args...)

			err := NewApp().Run(t.Context(), args)

			require.Error(t, err)
			require.ErrorContains(t, err, "was removed")
			require.ErrorContains(t, err, "--enable-accounts")
			require.ErrorContains(t, err, EnvBootstrapToken)
			require.ErrorContains(t, err, "/api/tokens")
			require.NotContains(t, err.Error(), secret, "the token value must never appear in errors")
			require.NotContains(t, err.Error(), "not defined", "must not be urfave's generic error")
			require.NoFileExists(t, dsn, "the refusal must precede opening the database")
		})
	}
}

func TestSessionTTLResolution(t *testing.T) {
	t.Run("neither flag uses the default", func(t *testing.T) {
		got, logs := runWithSessionTTL(t)

		require.Equal(t, time.Duration(DefaultSessionTTL)*time.Second, got)
		require.Empty(t, logs)
	})

	t.Run("deprecated cookie-max-age is honoured with a warning", func(t *testing.T) {
		got, logs := runWithSessionTTL(t, "--cookie-max-age", "3600")

		require.Equal(t, time.Hour, got)
		require.Contains(t, logs, "level=WARN")
		require.Contains(t, logs, "--session-ttl")
	})

	t.Run("deprecated environment variable is honoured", func(t *testing.T) {
		t.Setenv(EnvCookieMaxAge, "7200")

		got, logs := runWithSessionTTL(t)

		require.Equal(t, 2*time.Hour, got)
		require.Contains(t, logs, "level=WARN")
	})

	t.Run("explicit session-ttl wins over the deprecated flag", func(t *testing.T) {
		got, logs := runWithSessionTTL(t, "--cookie-max-age", "3600", "--session-ttl", "600")

		require.Equal(t, 10*time.Minute, got)
		require.Contains(t, logs, "level=WARN")
		require.Contains(t, logs, "ignored")
	})

	t.Run("session-ttl alone is silent", func(t *testing.T) {
		got, logs := runWithSessionTTL(t, "--session-ttl", "600")

		require.Equal(t, 10*time.Minute, got)
		require.Empty(t, logs)
	})
}
