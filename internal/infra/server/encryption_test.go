package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestEncryptionCommands_KeyLifecycle(t *testing.T) {
	directory := t.TempDir()
	dsn := filepath.Join(directory, "test.db")
	first := filepath.Join(directory, "first.json")
	added := filepath.Join(directory, "added.json")
	active := filepath.Join(directory, "active.json")
	retired := filepath.Join(directory, "retired.json")
	ctx := t.Context()
	run := func(keyring string, args ...string) (string, error) {
		app := NewApp()

		var output bytes.Buffer

		app.Writer = &output
		command := make([]string, 0, 8+len(args))
		command = append(command, "padmark", "encryption", "--storage", "sqlite", "--dsn", dsn,
			"--content-keyring-file", keyring)
		err := app.Run(ctx, append(command, args...))

		return output.String(), err
	}
	_, err := run("", "keygen", "--key-id", "first", "--output", first)
	require.NoError(t, err)
	_, err = run("", "keygen", "--key-id", "first", "--output", first)
	require.Error(t, err)
	require.NoError(t, NewApp().Run(ctx, []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	output, err := run(first, "status")
	require.NoError(t, err)
	require.Contains(t, output, `"state":"uninitialized"`)
	output, err = run(first, "migrate", "--dry-run")
	require.NoError(t, err)
	require.Contains(t, output, `"state":"uninitialized"`)

	_, err = run(first, "migrate")
	require.NoError(t, err)
	_, err = run(first, "key-add", "--key-id", "next", "--output", added)
	require.NoError(t, err)
	_, err = run(added, "key-activate", "--key-id", "next", "--output", active)
	require.ErrorContains(t, err, "not registered")
	_, err = run(added, "register", "--key-id", "next")
	require.NoError(t, err)
	_, err = run(added, "key-activate", "--key-id", "next", "--output", active)
	require.NoError(t, err)
	_, err = run(active, "rewrap")
	require.NoError(t, err)
	output, err = run(active, "key-remove", "--key-id", "first", "--output", retired)
	require.NoError(t, err)
	ring, err := crypto.LoadContentKeyring(retired)
	require.NoError(t, err)
	require.Equal(t, "next", ring.Active)
	require.Len(t, ring.Keys, 1)
	require.NotContains(t, output, base64.StdEncoding.EncodeToString(ring.Keys["next"]))

	_, err = run(retired, "key-remove", "--key-id", "next", "--output", filepath.Join(directory, "invalid.json"))
	require.ErrorContains(t, err, "active")
	_, err = run(retired, "key-add", "--dry-run", "--key-id", "bad", "--output", filepath.Join(directory, "invalid.json"))
	require.Error(t, err)
}

func TestServeRefusesSubstitutedContentKeyBeforeListening(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(t.Context(), "sqlite", dsn)
	require.NoError(t, err)

	defer db.Close()

	_, _, err = initRepos(t.Context(), "sqlite", db, newLogger("error", "text"))
	require.NoError(t, err)

	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	manager, err := newEncryptionManager(db, ring)
	require.NoError(t, err)
	require.NoError(t, manager.PrepareServe(t.Context()))

	ring.Keys["key"] = bytes.Repeat([]byte{2}, 32)
	file := filepath.Join(t.TempDir(), "wrong.json")
	require.NoError(t, crypto.WriteContentKeyring(file, ring))
	err = NewApp().Run(context.Background(), []string{"padmark", "serve", "--storage", "sqlite",
		"--dsn", dsn, "--content-keyring-file", file, "--addr", "invalid-listen-address"})
	require.ErrorContains(t, err, "does not match")
	require.NotContains(t, err.Error(), "listen")
}
