package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestServerRequiresContentKeyringBeforeOpeningDatabase(t *testing.T) {
	t.Setenv(EnvContentKeyring, "")

	for _, args := range [][]string{
		{"padmark", "--storage", "invalid"},
		{"padmark", "serve", "--storage", "invalid", "--content-keyring-file", filepath.Join(t.TempDir(), "missing")},
	} {
		err := NewApp().Run(context.Background(), args)
		require.ErrorContains(t, err, "content")
		require.NotContains(t, err.Error(), "unknown storage")
	}
}

func TestContentKeyringFlagAndEnv(t *testing.T) {
	t.Setenv(EnvContentKeyring, "/from/env.json")

	for _, item := range []struct {
		args []string
		want string
	}{
		{[]string{"padmark"}, "/from/env.json"},
		{[]string{"padmark", "--content-keyring-file", "/from/flag.json"}, "/from/flag.json"},
	} {
		app := clifactory.Command(func(command *cli.Command) {
			command.Flags = appFlags()
			command.Action = func(_ context.Context, cmd *cli.Command) error {
				require.Equal(t, item.want, cmd.String(FlagContentKeyring))

				return nil
			}
		})
		require.NoError(t, app.Run(context.Background(), item.args))
	}
}
