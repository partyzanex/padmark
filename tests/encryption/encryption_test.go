//go:build integration

package encryption_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

type snapshot func(*testing.T) *bun.DB

func TestSQLiteEncryption(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "source.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)
	runEncryptionSuite(t, db, func(t *testing.T) *bun.DB {
		t.Helper()
		path := filepath.Join(t.TempDir(), "restore.db")
		_, err := db.NewRaw("VACUUM INTO ?", path).Exec(t.Context())
		require.NoError(t, err)
		restored, err := sqliterepo.Open(path, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, restored.Close()) })

		return restored
	})
}

func TestPostgresEncryption(t *testing.T) {
	ctx := t.Context()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine", tcpostgres.WithDatabase("padmark_test"),
		tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = pgrepo.Migrate(ctx, db)
	require.NoError(t, err)
	runEncryptionSuite(t, db, func(t *testing.T) *bun.DB {
		t.Helper()

		for _, command := range [][]string{
			{"pg_dump", "-U", "test", "-d", "padmark_test", "-Fc", "-f", "/tmp/content-backup.dump"},
			{"createdb", "-U", "test", "restored"},
			{"pg_restore", "-U", "test", "-d", "restored", "/tmp/content-backup.dump"},
		} {
			code, _, err := container.Exec(t.Context(), command)
			require.NoError(t, err)
			require.Zero(t, code)
		}

		restoreDSN := strings.Replace(dsn, "/padmark_test", "/restored", 1)
		restored := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(restoreDSN))), pgdialect.New())

		t.Cleanup(func() { require.NoError(t, restored.Close()) })

		return restored
	})
}

func testKeyring() *crypto.ContentKeyring {
	return &crypto.ContentKeyring{Active: "old", Keys: map[string][]byte{"old": bytes.Repeat([]byte{41}, 32)}}
}

func manager(t *testing.T, store keys.Storage, ring *crypto.ContentKeyring) *keys.Manager {
	t.Helper()

	notes, err := ring.ForResource("note")
	require.NoError(t, err)
	links, err := ring.ForResource("link")
	require.NoError(t, err)

	return keys.New(store, ring, notes, links)
}

func reset(t *testing.T, db *bun.DB) {
	t.Helper()

	for _, table := range []string{"notes", "links", "content_encryption_keys", "content_encryption_state"} {
		_, err := db.NewDelete().Table(table).Where("1 = 1").Exec(t.Context())
		require.NoError(t, err)
	}
}

func insertLegacy(t *testing.T, db *bun.DB, resource, slug string) keys.Row {
	t.Helper()

	value, err := crypto.New().Encrypt("private content", slug)
	require.NoError(t, err)

	row := keys.Row{Resource: resource, ID: domain.HashSlug(slug), Value: value}
	if resource == "note" {
		_, err = db.NewRaw(`INSERT INTO notes
 (id,title,content,edit_code,views,burn_after_reading,burn_ttl,privacy,created_at,updated_at)
 VALUES (?, 'title', ?, 'hash', 7, true, 60, 'public', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			row.ID, value).Exec(t.Context())
	} else {
		_, err = db.NewRaw(`INSERT INTO links (id,target_url,edit_code,views,burn_after_click,privacy)
 VALUES (?, ?, 'hash', 9, true, 'public')`, row.ID, value).Exec(t.Context())
	}

	require.NoError(t, err)

	return row
}

func readRow(t *testing.T, repo *keyrepo.Repository, row keys.Row) keys.Row {
	t.Helper()
	current, err := repo.Get(t.Context(), row.Resource, row.ID)
	require.NoError(t, err)
	require.NotNil(t, current)

	return *current
}

func metadata(t *testing.T, db *bun.DB, table, id string) map[string]any {
	t.Helper()

	var record map[string]any
	require.NoError(t, db.NewSelect().Table(table).Where("id = ?", id).Scan(t.Context(), &record))
	delete(record, "content")
	delete(record, "target_url")

	return record
}

func runEncryptionSuite(t *testing.T, db *bun.DB, backup snapshot) {
	t.Helper()
	t.Run("concurrent_initialize_preserves_winner", func(t *testing.T) {
		testConcurrentInitialize(t, db)
	})
	t.Run("migration_rotation_restore", func(t *testing.T) {
		testMigrationRotationRestore(t, db, backup)
	})
	t.Run("restart_after_failure_and_pagination", func(t *testing.T) {
		testRestartAfterFailure(t, db)
	})
	t.Run("invalid_initial_record_counts", func(t *testing.T) {
		testInvalidInitialRecord(t, db)
	})
	t.Run("concurrent_update_and_delete_use_CAS", func(t *testing.T) {
		testConcurrentCAS(t, db)
	})
	t.Run("missing_substituted_keys_and_corruption", func(t *testing.T) {
		testKeysAndCorruption(t, db)
	})
	t.Run("prune_corrupt_raw_pagination_and_preservation", func(t *testing.T) {
		testPruneCorruptRaw(t, db)
	})
	t.Run("prune_concurrent_update_and_delete", func(t *testing.T) {
		testPruneConcurrentCAS(t, db)
	})
	t.Run("prune_partial_failure_resumes", func(t *testing.T) {
		testPrunePartialFailure(t, db)
	})
}

type interceptStorage struct {
	keys.Storage

	count  int
	before func(context.Context, keys.Row, int) error
}

func (s *interceptStorage) Replace(ctx context.Context, row keys.Row, value string) (bool, error) {
	s.count++

	err := s.before(ctx, row, s.count)
	if err != nil {
		return false, err
	}

	return s.Storage.Replace(ctx, row, value)
}

func testInvalidInitialRecord(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	repo := keyrepo.New(db)
	note := insertLegacy(t, db, "note", "invalid-initial-record")
	_, err := db.NewUpdate().Table("notes").Set("content = ?", "invalid").
		Where("id = ?", note.ID).Exec(t.Context())
	require.NoError(t, err)
	report, err := manager(t, repo, testKeyring()).Migrate(t.Context(), false)
	require.Error(t, err)
	require.Equal(t, 1, report.Failed)
	require.Equal(t, 1, report.CorruptRaw)
	require.Equal(t, keys.Pending, report.State)
	registry, err := repo.Registry(t.Context())
	require.NoError(t, err)
	require.NotNil(t, registry)
	require.Equal(t, keys.Pending, registry.Phase)
}

func makeCorruptRaw(t *testing.T, db *bun.DB, row keys.Row) {
	t.Helper()

	makeCorruptRawValue(t, db, row, "old plaintext")
}

// makeCorruptRawValue stores pre-encryption plaintext; values containing "$" without a
// "pmc<digits>$" version tag are still malformed raw records, never versioned envelopes.
func makeCorruptRawValue(t *testing.T, db *bun.DB, row keys.Row, value string) {
	t.Helper()

	table, column := "notes", "content"
	if row.Resource == "link" {
		table, column = "links", "target_url"
	}

	_, err := db.NewUpdate().Table(table).Set("? = ?", bun.Ident(column), value).
		Set("expires_at = ?", time.Now().Add(-time.Hour)).Where("id = ?", row.ID).Exec(t.Context())
	require.NoError(t, err)
}

//nolint:funlen // exercise cleanup, pagination, preservation and completing the pending migration together
func testPruneCorruptRaw(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	mgr := manager(t, repo, testKeyring())
	note := insertLegacy(t, db, "note", "keep-note")
	link := insertLegacy(t, db, "link", "keep-link")
	beforeNote := metadata(t, db, "notes", note.ID)
	beforeLink := metadata(t, db, "links", link.ID)

	for i := range 40 {
		makeCorruptRaw(t, db, insertLegacy(t, db, "note", fmt.Sprintf("corrupt-note-%d", i)))
	}

	makeCorruptRaw(t, db, insertLegacy(t, db, "link", "corrupt-link"))
	makeCorruptRawValue(t, db, insertLegacy(t, db, "note", "corrupt-dollar"), "costs $5; see $HOME and $x^2$")

	report, err := mgr.PruneCorrupt(ctx, true)
	require.NoError(t, err)
	require.Equal(t, 42, report.CorruptRaw)
	require.Zero(t, report.Pruned)

	registry, err := repo.Registry(ctx)
	require.NoError(t, err)
	require.Nil(t, registry)

	report, err = mgr.Migrate(ctx, false)
	require.ErrorContains(t, err, "prune --corrupt --dry-run")
	require.Equal(t, 2, report.Changed)
	require.Equal(t, 42, report.Failed)
	wrappedNote, wrappedLink := readRow(t, repo, note), readRow(t, repo, link)
	require.Error(t, mgr.PrepareServe(ctx))

	report, err = mgr.PruneCorrupt(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 42, report.Pruned)
	require.Zero(t, report.Failed)
	require.Zero(t, report.CorruptRaw)
	require.Equal(t, 2, report.Wrapped)
	require.Equal(t, keys.Pending, report.State)
	require.Equal(t, wrappedNote, readRow(t, repo, note))
	require.Equal(t, wrappedLink, readRow(t, repo, link))
	require.Equal(t, beforeNote, metadata(t, db, "notes", note.ID))
	require.Equal(t, beforeLink, metadata(t, db, "links", link.ID))
	require.Error(t, mgr.PrepareServe(ctx), "prune must not mark a pending migration ready")

	report, err = mgr.PruneCorrupt(ctx, false)
	require.NoError(t, err)
	require.Zero(t, report.Pruned)

	_, err = mgr.Migrate(ctx, false)
	require.NoError(t, err)
	require.NoError(t, mgr.PrepareServe(ctx))
}

type interceptDeleteStorage struct {
	keys.Storage

	before func(context.Context, keys.Row) error
}

func (s *interceptDeleteStorage) Delete(ctx context.Context, row keys.Row) (bool, error) {
	err := s.before(ctx, row)
	if err != nil {
		return false, err
	}

	return s.Storage.Delete(ctx, row)
}

func testPruneConcurrentCAS(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	ring := testKeyring()
	note := insertLegacy(t, db, "note", "prune-race-note")
	link := insertLegacy(t, db, "link", "prune-race-link")
	makeCorruptRaw(t, db, note)
	makeCorruptRaw(t, db, link)

	cipher, err := ring.ForResource("note")
	require.NoError(t, err)
	newer, err := cipher.Encrypt("new content", note.ID)
	require.NoError(t, err)

	store := &interceptDeleteStorage{Storage: repo, before: func(ctx context.Context, row keys.Row) error {
		if row.Resource == "note" {
			_, err := db.NewUpdate().Table("notes").Set("content = ?", newer).Set("views = views + 1").
				Where("id = ?", row.ID).Exec(ctx)

			return err
		}

		_, err := db.NewDelete().Table("links").Where("id = ?", row.ID).Exec(ctx)

		return err
	}}

	report, err := manager(t, store, ring).PruneCorrupt(ctx, false)
	require.NoError(t, err)
	require.Zero(t, report.Pruned)
	require.Equal(t, 2, report.Conflicts)
	require.Equal(t, newer, readRow(t, repo, note).Value)
	deleted, err := repo.Get(ctx, "link", link.ID)
	require.NoError(t, err)
	require.Nil(t, deleted)
}

func testPrunePartialFailure(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	repo := keyrepo.New(db)
	ring := testKeyring()

	for _, slug := range []string{"first-corrupt", "second-corrupt"} {
		makeCorruptRaw(t, db, insertLegacy(t, db, "note", slug))
	}

	count := 0
	store := &interceptDeleteStorage{Storage: repo, before: func(context.Context, keys.Row) error {
		count++
		if count == 2 {
			return errors.New("simulated delete failure")
		}

		return nil
	}}
	report, err := manager(t, store, ring).PruneCorrupt(t.Context(), false)
	require.ErrorContains(t, err, "simulated delete failure")
	require.Equal(t, 1, report.Pruned)
	require.Equal(t, 1, report.CorruptRaw)

	report, err = manager(t, repo, ring).PruneCorrupt(t.Context(), false)
	require.NoError(t, err)
	require.Equal(t, 1, report.Pruned)
	require.Zero(t, report.Failed)
}

//nolint:funlen // complete migration/rotation/restore lifecycle on each backend
func testMigrationRotationRestore(t *testing.T, db *bun.DB, backup snapshot) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	ring := testKeyring()
	mgr := manager(t, repo, ring)
	note := insertLegacy(t, db, "note", "legacy-note")
	link := insertLegacy(t, db, "link", "legacy-link")
	beforeNote := metadata(t, db, "notes", note.ID)
	beforeLink := metadata(t, db, "links", link.ID)
	report, err := mgr.Migrate(ctx, true)
	require.NoError(t, err)
	require.Equal(t, 2, report.Raw)

	registry, err := repo.Registry(ctx)
	require.NoError(t, err)
	require.Nil(t, registry)
	require.ErrorContains(t, mgr.PrepareServe(ctx), "migration required")
	require.Equal(t, note, readRow(t, repo, note))

	report, err = mgr.Migrate(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 2, report.Changed)
	require.Equal(t, 2, report.Wrapped)
	require.NoError(t, mgr.PrepareServe(ctx))
	require.Equal(t, beforeNote, metadata(t, db, "notes", note.ID))
	require.Equal(t, beforeLink, metadata(t, db, "links", link.ID))

	report, err = mgr.Migrate(ctx, false)
	require.NoError(t, err)
	require.Zero(t, report.Changed)
	wrappedNote := readRow(t, repo, note)
	wrappedLink := readRow(t, repo, link)
	restored := backup(t)
	ring.Keys["new"] = bytes.Repeat([]byte{42}, 32)

	require.NoError(t, mgr.Register(ctx, "new"))

	ring.Active = "new"
	mgr = manager(t, repo, ring)
	require.NoError(t, mgr.PrepareServe(ctx))
	require.ErrorContains(t, mgr.CanRemove(ctx, "old"), "still referenced")
	report, err = mgr.Rewrap(ctx, true)
	require.NoError(t, err)
	require.Zero(t, report.Changed)
	require.Equal(t, wrappedNote, readRow(t, repo, note))

	report, err = mgr.Rewrap(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 2, report.Changed)

	for _, row := range []keys.Row{wrappedNote, wrappedLink} {
		rotated := readRow(t, repo, row)
		require.Equal(t, strings.Split(row.Value, "$")[4], strings.Split(rotated.Value, "$")[4])
		require.Contains(t, rotated.Value, "pmc2$l$new$")
	}

	require.NoError(t, mgr.CanRemove(ctx, "old"))
	delete(ring.Keys, "old")
	mgr = manager(t, repo, ring)
	require.NoError(t, mgr.PrepareServe(ctx))
	require.Error(t, manager(t, keyrepo.New(restored), ring).PrepareServe(ctx))
	require.NoError(t, manager(t, keyrepo.New(restored), testKeyring()).PrepareServe(ctx))
	restoredNote := readRow(t, keyrepo.New(restored), note)
	restoredCipher, err := testKeyring().ForResource("note")
	require.NoError(t, err)
	plain, err := restoredCipher.Decrypt(restoredNote.Value, note.ID, "legacy-note")
	require.NoError(t, err)
	require.Equal(t, "private content", plain)

	report, err = mgr.ConvertSlugs(ctx, strings.NewReader("legacy-note\nlegacy-link\n"), true)
	require.NoError(t, err)
	require.Zero(t, report.Changed)
	report, err = mgr.ConvertSlugs(ctx, strings.NewReader("legacy-note\nlegacy-link\n"), false)
	require.NoError(t, err)
	require.Equal(t, 2, report.Native)
	require.Zero(t, report.Wrapped)

	for _, row := range []keys.Row{note, link} {
		cipher, err := ring.ForResource(row.Resource)
		require.NoError(t, err)
		value := readRow(t, repo, row).Value
		plain, err := cipher.ContentCipher.Decrypt(value, row.ID)
		require.NoError(t, err)
		require.Equal(t, "private content", plain)
	}

	require.Equal(t, beforeNote, metadata(t, db, "notes", note.ID))
	require.Equal(t, beforeLink, metadata(t, db, "links", link.ID))
	// Retain tombstones: a retired ID cannot be reused with different bytes.
	ring.Keys["old"] = bytes.Repeat([]byte{99}, 32)
	require.Error(t, manager(t, repo, ring).Register(ctx, "old"))
}

func testRestartAfterFailure(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	ring := testKeyring()

	for i := range 40 {
		insertLegacy(t, db, "note", fmt.Sprintf("note-%d", i))
	}

	insertLegacy(t, db, "link", "a-link")

	failing := &interceptStorage{Storage: repo, before: func(ctx context.Context, row keys.Row, index int) error {
		if index == 2 {
			return errors.New("simulated interruption")
		}

		return nil
	}}
	report, err := manager(t, failing, ring).Migrate(ctx, false)
	require.Error(t, err)
	require.Equal(t, 1, report.Changed)
	require.Equal(t, 40, report.Raw)
	require.Equal(t, 1, report.Wrapped)
	require.Error(t, manager(t, repo, ring).PrepareServe(ctx))
	report, err = manager(t, repo, ring).Migrate(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 40, report.Changed)
	require.Equal(t, 41, report.Wrapped)
	require.NoError(t, manager(t, repo, ring).PrepareServe(ctx))
	// Resume rotation after a committed batch/row, too.
	ring.Keys["next"] = bytes.Repeat([]byte{43}, 32)
	require.NoError(t, manager(t, repo, ring).Register(ctx, "next"))
	ring.Active = "next"
	failing.count = 0
	_, err = manager(t, failing, ring).Rewrap(ctx, false)
	require.Error(t, err)
	require.NoError(t, manager(t, repo, ring).PrepareServe(ctx))
	report, err = manager(t, repo, ring).Rewrap(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 40, report.Changed)
}

func testConcurrentCAS(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	ring := testKeyring()
	note := insertLegacy(t, db, "note", "race-note")
	link := insertLegacy(t, db, "link", "race-link")
	cipher, err := ring.ForResource("note")
	require.NoError(t, err)
	newer, err := cipher.Encrypt("concurrent update", note.ID)
	require.NoError(t, err)

	store := &interceptStorage{Storage: repo, before: func(ctx context.Context, row keys.Row, index int) error {
		if row.Resource == "note" {
			_, err := db.NewUpdate().Table("notes").Set("content = ?", newer).Set("views = views + 1").
				Where("id = ?", row.ID).Exec(ctx)

			return err
		}

		_, err := db.NewDelete().Table("links").Where("id = ?", row.ID).Exec(ctx)

		return err
	}}
	report, err := manager(t, store, ring).Migrate(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 2, report.Conflicts)
	require.Equal(t, newer, readRow(t, repo, note).Value)

	var views int
	require.NoError(t, db.NewSelect().Table("notes").Column("views").Where("id = ?", note.ID).Scan(ctx, &views))
	require.Equal(t, 8, views)

	deleted, err := repo.Get(ctx, "link", link.ID)
	require.NoError(t, err)
	require.Nil(t, deleted)
}

// requireUnprunableEnvelope checks that a failing versioned envelope is not reported as
// malformed raw data, is not pointed at prune, and blocks prune before any deletion.
func requireUnprunableEnvelope(t *testing.T, mgr *keys.Manager, report *keys.Report, err error) {
	t.Helper()

	require.Equal(t, 1, report.Failed)
	require.Zero(t, report.CorruptRaw, "an unknown envelope version is never malformed raw data")
	require.NotContains(t, err.Error(), "prune --corrupt", "prune cannot fix a failing envelope")

	pruned, err := mgr.PruneCorrupt(t.Context(), false)
	require.Error(t, err)
	require.Zero(t, pruned.Pruned)
}

func testKeysAndCorruption(t *testing.T, db *bun.DB) {
	t.Helper()

	reset(t, db)
	ctx := t.Context()
	repo := keyrepo.New(db)
	ring := testKeyring()
	mgr := manager(t, repo, ring)
	note := insertLegacy(t, db, "note", "sensitive-slug")
	_, err := mgr.Migrate(ctx, false)
	require.NoError(t, err)
	valid := readRow(t, repo, note)
	wrong := testKeyring()
	wrong.Keys["old"] = bytes.Repeat([]byte{50}, 32)
	require.Error(t, manager(t, repo, wrong).PrepareServe(ctx))
	registry, err := repo.Registry(ctx)
	require.NoError(t, err)
	require.Error(t, manager(t, repo, wrong).Register(ctx, "old"))
	unchanged, err := repo.Registry(ctx)
	require.NoError(t, err)
	require.Equal(t, registry, unchanged)

	_, err = db.NewUpdate().Table("notes").Set("content = ?", "pmc3$p$unknown$invalid$invalid").
		Where("id = ?", note.ID).Exec(ctx)
	require.NoError(t, err)
	require.Error(t, mgr.PrepareServe(ctx))
	report, err := mgr.Migrate(ctx, false)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sensitive-slug")
	requireUnprunableEnvelope(t, mgr, &report, err)

	registry, err = repo.Registry(ctx)
	require.NoError(t, err)
	require.Equal(t, keys.Pending, registry.Phase)

	_, err = db.NewUpdate().Table("notes").Set("content = ?", valid.Value).Where("id = ?", note.ID).Exec(ctx)
	require.NoError(t, err)
	require.Error(t, mgr.PrepareServe(ctx))
	_, err = mgr.Migrate(ctx, false)
	require.NoError(t, err)
	report, err = mgr.ConvertSlugs(ctx, strings.NewReader("missing-secret-slug\n"), false)
	require.Error(t, err)
	require.Equal(t, 1, report.Missing)
	require.NotContains(t, err.Error(), "missing-secret-slug")
}

func testConcurrentInitialize(t *testing.T, db *bun.DB) {
	t.Helper()
	reset(t, db)
	repo := keyrepo.New(db)
	registries := []*keys.Registry{
		{InstallationID: "11111111-1111-4111-8111-111111111111", Phase: keys.Pending,
			Checks: map[string]string{"first": "first-check"}},
		{InstallationID: "22222222-2222-4222-8222-222222222222", Phase: keys.Ready,
			Checks: map[string]string{"second": "second-check"}},
	}
	results := make([]error, len(registries))

	var group sync.WaitGroup
	for i, registry := range registries {
		group.Go(func() { results[i] = repo.Initialize(t.Context(), registry) })
	}

	group.Wait()

	for _, err := range results {
		require.NoError(t, err)
	}

	stored, err := repo.Registry(t.Context())
	require.NoError(t, err)
	require.NotNil(t, stored)

	winner := registries[0]
	if stored.InstallationID == registries[1].InstallationID {
		winner = registries[1]
	}

	require.Equal(t, winner, stored, "a losing initializer must not mix its state or key checks into the winner")
	require.NoError(t, repo.Initialize(t.Context(), registries[0]))
	require.NoError(t, repo.Initialize(t.Context(), registries[1]))
	unchanged, err := repo.Registry(t.Context())
	require.NoError(t, err)
	require.Equal(t, stored, unchanged)
}
