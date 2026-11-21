//go:build integration

package files_test

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/server"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

const keySize = 32

// installation is the persistent state of one server: the SQLite database and the keyring.
// A restart uses the same installation.
type installation struct {
	directory string
	dsn       string
	keyring   string
}

// newInstallation makes an empty installation with a random keyring. The active key is k1.
func newInstallation(t *testing.T) installation {
	t.Helper()

	directory := t.TempDir()
	key := make([]byte, keySize)
	_, err := rand.Read(key)
	require.NoError(t, err)

	keyring := filepath.Join(directory, "keys-k1.json")
	require.NoError(t, crypto.WriteContentKeyring(keyring,
		&crypto.ContentKeyring{Active: "k1", Keys: map[string][]byte{"k1": key}}))

	return installation{directory: directory, dsn: filepath.Join(directory, "padmark.db"), keyring: keyring}
}

// startServer starts serve with file publishing on and waits for readiness. The S3 keys of the
// test container go only through the environment, as in production.
func startServer(t *testing.T, binary, endpoint string, inst installation) (baseURL string, stop func()) {
	t.Helper()

	env := []string{server.EnvS3AccessKey + "=" + testAccessKey, server.EnvS3SecretKey + "=" + testSecretKey}

	return servertest.StartWithEnv(t, binary, inst.directory, env,
		"serve", "--storage", "sqlite", "--dsn", inst.dsn, "--content-keyring-file", inst.keyring,
		"--rate-limit", "0", "--log-level", "warn",
		"--s3-endpoint", endpoint, "--s3-bucket", testBucket, "--s3-path-style")
}

// runEncryption runs one `encryption` command in this process on the installation database with
// keyring and returns its standard output. The command needs no S3 settings.
func runEncryption(t *testing.T, inst installation, keyring string, command ...string) string {
	t.Helper()

	args := slices.Concat([]string{
		"padmark-server", "encryption", "--storage", "sqlite", "--dsn", inst.dsn, "--content-keyring-file", keyring,
	}, command)

	var output bytes.Buffer

	app := server.NewApp()
	app.Writer, app.ErrWriter = &output, &output
	require.NoError(t, app.Run(t.Context(), args), "encryption %v: %s", command, output.String())

	return output.String()
}
