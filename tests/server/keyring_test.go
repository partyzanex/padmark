package server_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/server"
)

func buildServer(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "padmark-server")
	//nolint:gosec // G204: repository source and output path in t.TempDir
	command := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-o", binary, "./cmd/padmark-server")
	command.Dir = "../.."
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	return binary
}

func serverEnvironment(allowShared string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "PADMARK_") {
			environment = append(environment, variable)
		}
	}

	return append(environment, server.EnvAllowSharedKeyring+"="+allowShared)
}

func startServer(t *testing.T, binary string, args, environment []string) (bool, string) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	args = append(args, "--addr", address, "--log-level", "warn")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var output bytes.Buffer

	command := exec.CommandContext(ctx, binary, args...) //nolint:gosec // test-built binary and controlled args
	command.Env, command.Stdout, command.Stderr = environment, &output, &output
	require.NoError(t, command.Start())

	done := make(chan struct{})

	go func() {
		_ = command.Wait()

		close(done)
	}()

	t.Cleanup(func() { cancel(); <-done })

	client := &http.Client{Timeout: 200 * time.Millisecond,
		Transport: nil, CheckRedirect: nil, Jar: nil,
	}
	defer client.CloseIdleConnections()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		response, requestErr := client.Get("http://" + address + "/readyz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				cancel()
				<-done

				return true, output.String()
			}
		}

		select {
		case <-done:
			return false, output.String()
		case <-ctx.Done():
			<-done
			t.Fatalf("server startup timed out: %s", output.String())
		case <-ticker.C:
		}
	}
}

func TestServerSharedKeyring(t *testing.T) {
	binary := buildServer(t)
	for _, test := range []struct {
		name, env       string
		args            []string
		derived, starts bool
	}{
		{name: "flag", args: []string{"--allow-shared-keyring"}, starts: true,
			env: "", derived: false,
		},
		{name: "env", args: []string{"serve"}, env: "true", starts: true,
			derived: false,
		},
		{name: "default_path_flag", args: []string{"serve", "--allow-shared-keyring"}, derived: true, starts: true,
			env: "",
		},
		{name: "default_path_env", env: "true", derived: true, starts: true,
			args: nil,
		},
		{name: "strict_default",
			env: "", args: nil, derived: false, starts: false,
		},
		{name: "flag_false_overrides_env", env: "true", args: []string{"serve", "--allow-shared-keyring=false"},
			derived: false, starts: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			keyringPath := filepath.Join(directory, "padmark-keys.json")
			ring := &crypto.ContentKeyring{Active: "test", Keys: map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)}}
			require.NoError(t, crypto.WriteContentKeyring(keyringPath, ring))
			require.NoError(t, os.Chmod(keyringPath, 0o644)) //nolint:gosec // G302: intentionally shared test keyring

			dsn := filepath.Join(directory, "test.db")

			args := append(test.args, "--storage", "sqlite", "--dsn", dsn)
			if !test.derived {
				args = append(args, "--content-keyring-file", keyringPath)
			}

			started, logs := startServer(t, binary, args, serverEnvironment(test.env))
			require.Equal(t, test.starts, started, logs)

			if test.starts {
				require.Contains(t, logs, "shared permissions explicitly allowed")
			} else {
				require.Contains(t, logs, crypto.ErrKeyringPermissions.Error())
				require.NoFileExists(t, dsn, "refuse the keyring before opening the database")
			}

			info, err := os.Stat(keyringPath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the operator's file mode is preserved")
		})
	}
}
