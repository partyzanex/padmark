package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// These tests pin whether a PostgreSQL DSN password can escape through the startup error path.
// Only the "server started" log line goes through redactDSN; every failure before it surfaces
// as the error serverAction/migrateAction returns, which cmd/padmark-server prints with
// slog.Error("run", "err", err). pgdriver.WithDSN parses the DSN inside an Option and PANICS on
// a parse error instead of returning it, and url.Parse errors echo the whole raw URL — password
// included — so a malformed DSN crashes the process with the password in the panic message.
//
// Cases marked parseTime are the confirmed defects: they fail today and pin the desired
// behaviour (openDB returns an error and nothing it produces contains the password).

// leakTestPassword is assembled at runtime so no literal looks like a hardcoded credential
// (gosec G101); it is distinctive enough that any substring match is a real leak.
func leakTestPassword() string { return strings.Join([]string{"Sup3r", "S3cret", "PW"}, "-") }

const leakMarker = "<LEAKED-PASSWORD>"

type dsnLeakCase struct {
	name string
	dsn  string
	// parseTime marks DSNs rejected while pgdriver parses them (today: a panic).
	parseTime bool
}

func dsnLeakCases(secret string) []dsnLeakCase {
	creds := "padmark:" + secret

	return []dsnLeakCase{
		{
			name: "connection refused", dsn: "postgres://" + creds + "@127.0.0.1:1/padmark?sslmode=disable",
			parseTime: false,
		},
		{
			name: "unresolvable host",
			dsn:  "postgres://" + creds + "@padmark-db.invalid:5432/padmark?sslmode=disable&dial_timeout=3s",

			parseTime: false,
		},
		{
			name: "out of range port", dsn: "postgres://" + creds + "@127.0.0.1:99999/padmark?sslmode=disable",
			parseTime: false,
		},
		{name: "unsupported sslmode", dsn: "postgres://" + creds + "@127.0.0.1:1/padmark?sslmode=bogus", parseTime: true},
		{name: "invalid timeout", dsn: "postgres://" + creds + "@127.0.0.1:1/padmark?timeout=forever", parseTime: true},
		{name: "non-numeric port", dsn: "postgres://" + creds + "@127.0.0.1:abc/padmark", parseTime: true},
		{name: "bad percent escape in password", dsn: "postgres://" + creds + "%zz@127.0.0.1:1/padmark", parseTime: true},
		{name: "space in host", dsn: "postgres://" + creds + "@db host:5432/padmark", parseTime: true},
		{name: "unix scheme without socket path", dsn: "unix://" + creds + "@padmark", parseTime: true},
		{
			name:      "key-value dsn",
			dsn:       "host=127.0.0.1 port=1 user=padmark password=" + secret + " dbname=padmark",
			parseTime: true,
		},
		{
			name:      "key-value dsn with colon in password",
			dsn:       "host=127.0.0.1 port=1 user=padmark password=" + secret + ":x dbname=padmark",
			parseTime: true,
		},
	}
}

// callRecovering runs call and returns the text a caller would get to see — the error message, or
// the panic value the runtime would print on crash — and whether call panicked.
func callRecovering(call func() error) (text string, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			text, panicked = fmt.Sprint(rec), true
		}
	}()

	err := call()
	if err != nil {
		return err.Error(), false
	}

	return "", false
}

func TestOpenDB_PostgresDSNPasswordNeverInErrorOrPanic(t *testing.T) {
	secret := leakTestPassword()

	for _, testCase := range dsnLeakCases(secret) {
		t.Run(testCase.name, func(t *testing.T) {
			text, panicked := callRecovering(func() error {
				db, err := openDB(t.Context(), storagePostgres, testCase.dsn, slog.New(slog.DiscardHandler))
				if err == nil {
					_ = db.Close()
				}

				return err
			})

			shown := strings.ReplaceAll(text, secret, leakMarker)
			assert.False(t, panicked, "openDB must return an error, not panic; panic value: %s", shown)
			assert.NotEmpty(t, text, "an unreachable or invalid DSN must fail")
			assert.NotContains(t, text, secret, "DSN password leaked: %s", shown)
		})
	}
}

// TestRedactDSN_MalformedURLNeverEchoesPassword: redactDSN falls back to returning the DSN
// verbatim when url.Parse fails and the string has no "password=" key. Today the only caller
// logs after a successful openDB (which panics first on these inputs), so this is latent, but
// the function's contract — "removes credentials from a DSN before logging" — is broken.
func TestRedactDSN_MalformedURLNeverEchoesPassword(t *testing.T) {
	secret := leakTestPassword()

	for _, dsn := range []string{
		"postgres://padmark:" + secret + "%zz@db.example.com/padmark",
		"postgres://padmark:" + secret + "@db.example.com:abc/padmark",
	} {
		got := redactDSN(dsn)
		assert.NotContains(t, got, secret, "redactDSN leaked: %s", strings.ReplaceAll(got, secret, leakMarker))
	}
}

// captureServerOutput swaps os.Stderr (newLogger binds it at call time) and the default slog
// logger (migrate uses slog.Default) for the duration of body and returns everything written.
func captureServerOutput(t *testing.T, body func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	origStderr, origLogger := os.Stderr, slog.Default()
	os.Stderr = writer

	var buf bytes.Buffer

	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))

	done := make(chan []byte)

	go func() {
		data, _ := io.ReadAll(reader)
		done <- data
	}()

	defer func() {
		os.Stderr = origStderr

		slog.SetDefault(origLogger)
	}()

	body()

	require.NoError(t, writer.Close())

	return buf.String() + string(<-done)
}

// TestServeAndMigrateCommands_PostgresDSNPasswordNeverEscapes runs the real CLI entry points
// (padmark serve / padmark migrate) the way cmd/padmark-server does and checks the final error
// (what main logs), any panic, and everything the commands wrote to stderr or slog.
func TestServeAndMigrateCommands_PostgresDSNPasswordNeverEscapes(t *testing.T) {
	secret := leakTestPassword()
	keyring := filepath.Join(t.TempDir(), "keyring.json")
	require.NoError(t, crypto.WriteContentKeyring(keyring, &crypto.ContentKeyring{
		Active: "leak-test", Keys: map[string][]byte{"leak-test": bytes.Repeat([]byte{7}, 32)},
	}))

	for _, testCase := range dsnLeakCases(secret) {
		for _, command := range []string{"serve", "migrate"} {
			t.Run(command+"/"+testCase.name, func(t *testing.T) {
				args := []string{"padmark", command, "--storage", storagePostgres, "--dsn", testCase.dsn}
				if command == "serve" {
					args = append(args, "--content-keyring-file", keyring, "--addr", "127.0.0.1:0")
				}

				var (
					text     string
					panicked bool
				)

				output := captureServerOutput(t, func() {
					text, panicked = callRecovering(func() error {
						return NewApp().Run(context.WithoutCancel(t.Context()), args)
					})
				})

				shown := strings.ReplaceAll(text, secret, leakMarker)
				assert.False(t, panicked, "%s must fail with an error, not crash; panic value: %s", command, shown)
				assert.NotEmpty(t, text, "an unreachable or invalid DSN must fail startup")
				assert.NotContains(t, text, secret, "DSN password leaked in the returned error: %s", shown)
				assert.NotContains(t, output, secret, "DSN password leaked in logs: %s",
					strings.ReplaceAll(output, secret, leakMarker))
			})
		}
	}
}
