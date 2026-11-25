package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

// hashShapedEditCode has the exact shape of a stored "s1$" hash (22 + 43 base64url characters).
const hashShapedEditCode = "s1$" + "AAAAAAAAAAAAAAAAAAAAAA" + "$" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

type EncryptionCommandSuite struct {
	suite.Suite
}

func TestEncryptionCommandSuite(t *testing.T) {
	suite.Run(t, new(EncryptionCommandSuite))
}

// encryptionRun runs one "padmark encryption" subcommand against the SQLite database dsn with the
// keyring file keyring. It returns the output of the command.
type encryptionRun func(keyring string, args ...string) (string, error)

// encryptionRunner returns the encryptionRun of the database dsn.
func (s *EncryptionCommandSuite) encryptionRunner(dsn string) encryptionRun {
	return func(keyring string, args ...string) (string, error) {
		app := NewApp()

		var output bytes.Buffer

		app.Writer = &output
		command := make([]string, 0, 8+len(args))
		command = append(command, "padmark", "encryption", "--storage", "sqlite", "--dsn", dsn,
			"--content-keyring-file", keyring)
		err := app.Run(s.T().Context(), append(command, args...))

		return output.String(), err
	}
}

func (s *EncryptionCommandSuite) TestEncryptionCommands_KeyLifecycle() {
	directory := s.T().TempDir()
	dsn := filepath.Join(directory, "test.db")
	first := filepath.Join(directory, "first.json")
	added := filepath.Join(directory, "added.json")
	active := filepath.Join(directory, "active.json")
	retired := filepath.Join(directory, "retired.json")
	ctx := s.T().Context()
	run := s.encryptionRunner(dsn)
	_, err := run("", "keygen", "--key-id", "first", "--output", first)
	s.Require().NoError(err)
	_, err = run("", "keygen", "--key-id", "first", "--output", first)
	s.Require().Error(err)
	s.Require().NoError(NewApp().Run(ctx, []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	output, err := run(first, "status")
	s.Require().NoError(err)
	s.Require().Contains(output, `"state":"uninitialized"`)
	output, err = run(first, "migrate", "--dry-run")
	s.Require().NoError(err)
	s.Require().Contains(output, `"state":"uninitialized"`)

	_, err = run(first, "migrate")
	s.Require().NoError(err)
	_, err = run(first, "key-add", "--key-id", "next", "--output", added)
	s.Require().NoError(err)
	_, err = run(added, "key-activate", "--key-id", "next", "--output", active)
	s.Require().ErrorContains(err, "not registered")
	_, err = run(added, "register", "--key-id", "next")
	s.Require().NoError(err)
	_, err = run(added, "key-activate", "--key-id", "next", "--output", active)
	s.Require().NoError(err)
	_, err = run(active, "rewrap")
	s.Require().NoError(err)
	s.checkKeyRemoval(run, directory, active, retired)
}

// checkKeyRemoval removes the first key from the keyring active into retired and checks that
// the active key stays and that the commands refuse to remove it.
func (s *EncryptionCommandSuite) checkKeyRemoval(run encryptionRun, directory, active, retired string) {
	output, err := run(active, "key-remove", "--key-id", "first", "--output", retired)
	s.Require().NoError(err)
	ring, err := loadContentKeyring(retired, false, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Require().Equal("next", ring.Active)
	s.Require().Len(ring.Keys, 1)
	s.Require().NotContains(output, base64.StdEncoding.EncodeToString(ring.Keys["next"]))

	_, err = run(retired, "key-remove", "--key-id", "next", "--output", filepath.Join(directory, "invalid.json"))
	s.Require().ErrorContains(err, "active")
	_, err = run(retired, "key-add", "--dry-run", "--key-id", "bad", "--output", filepath.Join(directory, "invalid.json"))
	s.Require().Error(err)
}

func (s *EncryptionCommandSuite) TestServeRefusesSubstitutedContentKeyBeforeListening() {
	dsn := filepath.Join(s.T().TempDir(), "test.db")
	db, err := openDB(s.T().Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	defer db.Close()

	_, err = initRepos(s.T().Context(), "sqlite", db, newLogger("error", "text"),
		migrationOptions{dsn: dsn, skipBackup: true})
	s.Require().NoError(err)

	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	manager, err := newEncryptionManager(db, ring, crypto.NewEditCodeHasher(crypto.Argon2Params{
		Memory: 0, Time: 0, Threads: 0,
	}, slog.New(slog.DiscardHandler)))
	s.Require().NoError(err)
	s.Require().NoError(manager.PrepareServe(s.T().Context()))

	ring.Keys["key"] = bytes.Repeat([]byte{2}, 32)
	file := filepath.Join(s.T().TempDir(), "wrong.json")
	s.Require().NoError(crypto.WriteContentKeyring(file, ring))
	err = NewApp().Run(s.T().Context(), []string{
		"padmark", "serve", "--storage", "sqlite",
		"--dsn", dsn, "--content-keyring-file", file, "--addr", "invalid-listen-address",
	})
	s.Require().ErrorContains(err, "does not match")
	s.Require().NotContains(err.Error(), "listen")
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
func (s *EncryptionCommandSuite) TestEncryptionMigrateConvertsV016Database() {
	directory := s.T().TempDir()
	dsn, keyfile := filepath.Join(directory, "v016.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(s.T(), dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	s.Require().NoError(err)

	dump, err := os.ReadFile("../../../tests/fixtures/v0.1.6/sqlite.sql")
	s.Require().NoError(err)
	loader, err := openDB(s.T().Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	_, err = loader.ExecContext(s.T().Context(), string(dump))
	s.Require().NoError(err)
	s.Require().NoError(loader.Close())
	s.Require().NoError(NewApp().Run(s.T().Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	output, err := runEncryptionTestCommand(s.T(), dsn, keyfile, "status")
	s.Require().NoError(err)
	s.Require().Contains(output, `"plaintext":7`)

	output, err = runEncryptionTestCommand(s.T(), dsn, keyfile, "migrate")
	s.Require().NoError(err)
	report := encryptionTestReport(s.T(), output)
	s.Require().Equal(contentkeys.Ready, report.State)
	s.Require().Equal(7, report.Changed)
	s.Require().Zero(report.Plaintext)
	s.Require().NotContains(output, "my-custom_Slug-1")

	ring, err := loadContentKeyring(keyfile, false, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	db, err := openDB(s.T().Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	s.Require().NoError(validateContentEncryption(s.T().Context(), db, ring,
		crypto.NewEditCodeHasher(crypto.Argon2Params{
			Memory: 0, Time: 0, Threads: 0,
		}, slog.New(slog.DiscardHandler)), slog.New(slog.DiscardHandler)))
}

func TestEncryptionPruneRequiresExplicitCorruptFlag(t *testing.T) {
	// Rejection happens before opening the keyring or database, even in dry-run.
	for _, args := range [][]string{{"prune"}, {"prune", "--dry-run"}} {
		_, err := runEncryptionTestCommand(t, "not-a-database", "not-a-keyring", args...)
		require.ErrorContains(t, err, "prune requires --corrupt")
	}
}

func (s *EncryptionCommandSuite) TestEncryptionPruneRecoversExpiredPlaintextDatabase() {
	directory := s.T().TempDir()
	dsn, keyfile := filepath.Join(directory, "legacy.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(s.T(), dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	s.Require().NoError(err)
	s.Require().NoError(NewApp().Run(s.T().Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	db, err := openDB(s.T().Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	expired := time.Now().UTC().Add(-time.Hour)
	// A malformed row of a v0.2.0–v0.3.3 database: its edit code has the stored-hash shape, so it
	// is corrupt raw data, not an untagged v0.1.6 note (which prune never deletes).
	_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at,expires_at)
 VALUES ('old-id', 'old', ?, ?, 7, false, 0, 'public', ?, ?, ?)`,
		// "$" in pre-encryption plaintext must not make the row look like a versioned envelope.
		"sensitive old plaintext costing $5 in $HOME", hashShapedEditCode, expired, expired, expired).Exec(s.T().Context())
	s.Require().NoError(err)
	output, err := runEncryptionTestCommand(s.T(), dsn, keyfile, "migrate")
	s.Require().ErrorContains(err, "prune --corrupt --dry-run")
	s.Require().Equal(1, strings.Count(err.Error(), "content encryption:"))
	report := encryptionTestReport(s.T(), output)
	s.Require().Equal(contentkeys.Pending, report.State)
	s.Require().Equal(1, report.CorruptRaw)
	s.Require().Equal(1, report.Failed)
	s.Require().NotContains(output, "sensitive old plaintext")

	assertEncryptionPruneRecovery(s.T(), dsn, keyfile)
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
		}, slog.New(slog.DiscardHandler)), slog.New(slog.DiscardHandler)))
	missing, err := keyrepo.New(db).Get(t.Context(), "note", "old-id")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func (s *EncryptionCommandSuite) TestValidateContentEncryptionAddsContextOnce() {
	// No schema migration: reading the registry fails inside PrepareServe.
	db, err := openDB(s.T().Context(), "sqlite", filepath.Join(s.T().TempDir(), "empty.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	err = validateContentEncryption(s.T().Context(), db, ring, crypto.NewEditCodeHasher(crypto.Argon2Params{
		Memory: 0, Time: 0, Threads: 0,
	}, slog.New(slog.DiscardHandler)),
		slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Require().True(strings.HasPrefix(err.Error(), "content encryption: "), err.Error())
	s.Require().Equal(1, strings.Count(err.Error(), "content encryption:"), err.Error())
}

// TestEncryptionMarkLegacyConvertsHashShapedV016Note: a v0.1.6 note whose custom slug is 64 hex
// characters and whose edit code has the exact stored-hash shape is listed by the prune preview,
// tagged by mark-legacy and converted by migrate; it is never deleted (review R1).
func (s *EncryptionCommandSuite) TestEncryptionMarkLegacyConvertsHashShapedV016Note() {
	directory := s.T().TempDir()
	dsn, keyfile := filepath.Join(directory, "legacy.db"), filepath.Join(directory, "keys.json")
	_, err := runEncryptionTestCommand(s.T(), dsn, "", "keygen", "--key-id", "key", "--output", keyfile)
	s.Require().NoError(err)
	s.Require().NoError(NewApp().Run(s.T().Context(), []string{"padmark", "migrate", "--storage", "sqlite", "--dsn", dsn}))

	db, err := openDB(s.T().Context(), "sqlite", dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	slug, now := strings.Repeat("c", 64), time.Now().UTC()
	_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at)
 VALUES (?, 'v0.1.6', 'plaintext of a v0.1.6 note', ?, 0, false, 0, 'public', ?, ?)`,
		slug, hashShapedEditCode, now, now).Exec(s.T().Context())
	s.Require().NoError(err)

	_, err = runEncryptionTestCommand(s.T(), dsn, keyfile, "migrate")
	s.Require().ErrorContains(err, "mark-legacy --id")

	output, err := runEncryptionTestCommand(s.T(), dsn, keyfile, "prune", "--corrupt", "--dry-run")
	s.Require().NoError(err)
	s.Require().Equal([]contentkeys.Candidate{{Resource: "note", ID: slug, Reason: "corrupt_raw"}},
		encryptionTestReport(s.T(), output).Candidates)

	_, err = runEncryptionTestCommand(s.T(), dsn, keyfile, "mark-legacy")
	s.Require().ErrorContains(err, "requires --id")

	output, err = runEncryptionTestCommand(s.T(), dsn, keyfile, "mark-legacy", "--id", slug)
	s.Require().NoError(err)
	s.Require().Equal(1, encryptionTestReport(s.T(), output).Plaintext)

	output, err = runEncryptionTestCommand(s.T(), dsn, keyfile, "migrate")
	s.Require().NoError(err)
	report := encryptionTestReport(s.T(), output)
	s.Require().Equal(contentkeys.Ready, report.State)
	s.Require().Equal(1, report.Native)

	var ids []string
	s.Require().NoError(db.NewSelect().Table("notes").Column("id").Scan(s.T().Context(), &ids))
	s.Require().Equal([]string{domain.HashSlug(slug)}, ids, "the note must be converted, not deleted")
}
