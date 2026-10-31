package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

// hashShapedEditCode has the exact shape of a stored "s1$" hash (22 + 43 base64url characters).
const hashShapedEditCode = "s1$" + "AAAAAAAAAAAAAAAAAAAAAA" + "$" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

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

	_, _, err = initRepos(t.Context(), "sqlite", db, newLogger("error", "text"),
		migrationOptions{dsn: dsn, skipBackup: true})
	require.NoError(t, err)

	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	manager, err := newEncryptionManager(db, ring, crypto.NewEditCodeHasher(crypto.Argon2Params{
		Memory: 0, Time: 0, Threads: 0,
	}))
	require.NoError(t, err)
	require.NoError(t, manager.PrepareServe(t.Context()))

	ring.Keys["key"] = bytes.Repeat([]byte{2}, 32)
	file := filepath.Join(t.TempDir(), "wrong.json")
	require.NoError(t, crypto.WriteContentKeyring(file, ring))
	err = NewApp().Run(context.Background(), []string{
		"padmark", "serve", "--storage", "sqlite",
		"--dsn", dsn, "--content-keyring-file", file, "--addr", "invalid-listen-address",
	})
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

// The documented upgrade from v0.1.6 through the real CLI: migrate, encryption status,
// encryption migrate, then the serve gate. tests/upgrade covers the converted data in depth.
func TestEncryptionMigrateConvertsV016Database(t *testing.T) {
	directory := t.TempDir()
	dsn, keyfile := filepath.Join(directory, "v016.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(t, dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	require.NoError(t, err)

	dump, err := os.ReadFile("../../../tests/fixtures/v0.1.6/sqlite.sql")
	require.NoError(t, err)
	loader, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, err = loader.ExecContext(t.Context(), string(dump))
	require.NoError(t, err)
	require.NoError(t, loader.Close())
	require.NoError(t, NewApp().Run(t.Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	output, err := runEncryptionTestCommand(t, dsn, keyfile, "status")
	require.NoError(t, err)
	require.Contains(t, output, `"plaintext":7`)

	output, err = runEncryptionTestCommand(t, dsn, keyfile, "migrate")
	require.NoError(t, err)
	report := encryptionTestReport(t, output)
	require.Equal(t, contentkeys.Ready, report.State)
	require.Equal(t, 7, report.Changed)
	require.Zero(t, report.Plaintext)
	require.NotContains(t, output, "my-custom_Slug-1")

	ring, err := loadContentKeyring(keyfile, false, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	db, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, validateContentEncryption(t.Context(), db, ring,
		crypto.NewEditCodeHasher(crypto.Argon2Params{
			Memory: 0, Time: 0, Threads: 0,
		}), slog.New(slog.DiscardHandler)))
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
	// A malformed row of a v0.2.0–v0.3.3 database: its edit code has the stored-hash shape, so it
	// is corrupt raw data, not an untagged v0.1.6 note (which prune never deletes).
	_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at,expires_at)
 VALUES ('old-id', 'old', ?, ?, 7, false, 0, 'public', ?, ?, ?)`,
		// "$" in pre-encryption plaintext must not make the row look like a versioned envelope.
		"sensitive old plaintext costing $5 in $HOME", hashShapedEditCode, expired, expired, expired).Exec(t.Context())
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
	require.NoError(t, validateContentEncryption(t.Context(), db, ring,
		crypto.NewEditCodeHasher(crypto.Argon2Params{
			Memory: 0, Time: 0, Threads: 0,
		}), slog.New(slog.DiscardHandler)))
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
	err = validateContentEncryption(t.Context(), db, ring, crypto.NewEditCodeHasher(crypto.Argon2Params{
		Memory: 0, Time: 0, Threads: 0,
	}),
		slog.New(slog.DiscardHandler))
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "content encryption: "), err.Error())
	require.Equal(t, 1, strings.Count(err.Error(), "content encryption:"), err.Error())
}

// TestEncryptionMarkLegacyConvertsHashShapedV016Note: a v0.1.6 note whose custom slug is 64 hex
// characters and whose edit code has the exact stored-hash shape is listed by the prune preview,
// tagged by mark-legacy and converted by migrate; it is never deleted (review R1).
func TestEncryptionMarkLegacyConvertsHashShapedV016Note(t *testing.T) {
	directory := t.TempDir()
	dsn, keyfile := filepath.Join(directory, "legacy.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(t, dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	require.NoError(t, err)
	require.NoError(t, NewApp().Run(t.Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	db, err := openDB(t.Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	slug, now := strings.Repeat("c", 64), time.Now().UTC()
	_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at)
 VALUES (?, 'v0.1.6', 'plaintext of a v0.1.6 note', ?, 0, false, 0, 'public', ?, ?)`,
		slug, hashShapedEditCode, now, now).Exec(t.Context())
	require.NoError(t, err)

	_, err = runEncryptionTestCommand(t, dsn, keyfile, "migrate")
	require.ErrorContains(t, err, "mark-legacy --id")

	output, err := runEncryptionTestCommand(t, dsn, keyfile, "prune", "--corrupt", "--dry-run")
	require.NoError(t, err)
	require.Equal(t, []contentkeys.Candidate{{Resource: "note", ID: slug, Reason: "corrupt_raw"}},
		encryptionTestReport(t, output).Candidates)

	_, err = runEncryptionTestCommand(t, dsn, keyfile, "mark-legacy")
	require.ErrorContains(t, err, "requires --id")

	output, err = runEncryptionTestCommand(t, dsn, keyfile, "mark-legacy", "--id", slug)
	require.NoError(t, err)
	require.Equal(t, 1, encryptionTestReport(t, output).Plaintext)

	output, err = runEncryptionTestCommand(t, dsn, keyfile, "migrate")
	require.NoError(t, err)
	report := encryptionTestReport(t, output)
	require.Equal(t, contentkeys.Ready, report.State)
	require.Equal(t, 1, report.Native)

	var ids []string
	require.NoError(t, db.NewSelect().Table("notes").Column("id").Scan(t.Context(), &ids))
	require.Equal(t, []string{domain.HashSlug(slug)}, ids, "the note must be converted, not deleted")
}
