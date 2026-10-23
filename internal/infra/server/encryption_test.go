package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
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
	ring, err := loadContentKeyring(retired, false, slog.New(slog.DiscardHandler))
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
	db, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
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

func runEncryptionTestCommand(t *testing.T, dsn, keyring string, args ...string) (string, error) {
	t.Helper()

	app := NewApp()

	var output bytes.Buffer

	app.Writer = &output
	command := make([]string, 0, 8+len(args))
	command = append(command, "padmark", "encryption", "--storage", "sqlite", "--dsn", dsn,
		"--content-keyring-file", keyring)
	err := app.Run(t.Context(), append(command, args...))

	return output.String(), err
}

func encryptionTestReport(t *testing.T, output string) contentkeys.Report {
	t.Helper()

	var report contentkeys.Report

	require.NoError(t, json.Unmarshal([]byte(output), &report))

	return report
}

func TestEncryptionPruneRequiresExplicitCorruptFlag(t *testing.T) {
	// Rejection happens before opening the keyring or database, even in dry-run.
	for _, args := range [][]string{{"prune"}, {"prune", "--dry-run"}} {
		_, err := runEncryptionTestCommand(t, "not-a-database", "not-a-keyring", args...)
		require.ErrorContains(t, err, "prune requires --corrupt")
	}
}

func TestEncryptionPruneRecoversExpiredPlaintextDatabase(t *testing.T) {
	directory := t.TempDir()
	dsn, keyfile := filepath.Join(directory, "legacy.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(t, dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	require.NoError(t, err)
	require.NoError(t, NewApp().Run(t.Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	db, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	expired := time.Now().UTC().Add(-time.Hour)
	_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at,expires_at)
 VALUES ('old-id', 'old', ?, 'hash', 7, false, 0, 'public', ?, ?, ?)`,
		// "$" in pre-encryption plaintext must not make the row look like a versioned envelope.
		"sensitive old plaintext costing $5 in $HOME", expired, expired, expired).Exec(t.Context())
	require.NoError(t, err)
	output, err := runEncryptionTestCommand(t, dsn, keyfile, "migrate")
	require.ErrorContains(t, err, "prune --corrupt --dry-run")
	require.Equal(t, 1, strings.Count(err.Error(), "content encryption:"))
	report := encryptionTestReport(t, output)
	require.Equal(t, contentkeys.Pending, report.State)
	require.Equal(t, 1, report.CorruptRaw)
	require.Equal(t, 1, report.Failed)
	require.NotContains(t, output, "sensitive old plaintext")

	assertEncryptionPruneRecovery(t, dsn, keyfile)
}

func assertEncryptionPruneRecovery(t *testing.T, dsn, keyfile string) {
	t.Helper()

	output, err := runEncryptionTestCommand(t, dsn, keyfile, "prune", "--corrupt", "--dry-run")
	require.NoError(t, err)
	report := encryptionTestReport(t, output)
	require.Equal(t, 1, report.CorruptRaw)
	require.Zero(t, report.Pruned)

	output, err = runEncryptionTestCommand(t, dsn, keyfile, "prune", "--corrupt")
	require.NoError(t, err)
	report = encryptionTestReport(t, output)
	require.Equal(t, 1, report.Pruned)
	require.Zero(t, report.Failed)
	require.Equal(t, contentkeys.Pending, report.State)

	output, err = runEncryptionTestCommand(t, dsn, keyfile, "migrate")
	require.NoError(t, err)
	report = encryptionTestReport(t, output)
	require.Equal(t, contentkeys.Ready, report.State)

	ring, err := loadContentKeyring(keyfile, false, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	db, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, validateContentEncryption(t.Context(), db, ring))
	missing, err := keyrepo.New(db).Get(t.Context(), "note", "old-id")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestValidateContentEncryptionAddsContextOnce(t *testing.T) {
	// No schema migration: reading the registry fails inside PrepareServe.
	db, err := openDB(t.Context(), "sqlite", filepath.Join(t.TempDir(), "empty.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	err = validateContentEncryption(t.Context(), db, ring)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "content encryption: "), err.Error())
	require.Equal(t, 1, strings.Count(err.Error(), "content encryption:"), err.Error())
}
