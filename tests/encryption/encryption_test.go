//go:build integration

package encryption_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)
	runEncryptionSuite(t, db, func(t *testing.T) *bun.DB {
		t.Helper()
		path := filepath.Join(t.TempDir(), "restore.db")
		_, err := db.NewRaw("VACUUM INTO ?", path).Exec(t.Context())
		require.NoError(t, err)
		restored, err := sqliterepo.Open(path)
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
	require.Equal(t, "uninitialized", report.State)
	registry, err := repo.Registry(t.Context())
	require.NoError(t, err)
	require.Nil(t, registry)
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
	require.Equal(t, 1, report.Failed)
	require.NotContains(t, err.Error(), "sensitive-slug")
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
