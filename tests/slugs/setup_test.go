//go:build integration

package slugs_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

// SlugNamespaceSuite runs the slug namespace scenarios on one backend. The scenarios share one
// database and run in a fixed order (runSlugNamespace), so the suite has one test method.
type SlugNamespaceSuite struct {
	suite.Suite

	// prepare returns the opener of the database of the backend. Each call of the opener gives
	// a new connection pool; the first pool has applied the migrations.
	prepare func(t *testing.T) func(*testing.T) *bun.DB
	db      *bun.DB
	open    func(*testing.T) *bun.DB
	objects *s3store.Store
}

func TestSQLiteSlugNamespace(t *testing.T) {
	namespace := new(SlugNamespaceSuite)
	namespace.prepare = prepareSQLite
	suite.Run(t, namespace)
}

func TestPostgresSlugNamespace(t *testing.T) {
	namespace := new(SlugNamespaceSuite)
	namespace.prepare = preparePostgres
	suite.Run(t, namespace)
}

func (s *SlugNamespaceSuite) SetupSuite() {
	s.open = s.prepare(s.T())
	s.db = s.open(s.T())
	s.objects = testenv.S3Mock(s.T()).Store(s.T())
}

func (s *SlugNamespaceSuite) TestNamespace() {
	runSlugNamespace(s.T(), s.db, s.open, s.objects)
}

// prepareSQLite makes a migrated SQLite file database and returns its opener.
func prepareSQLite(t *testing.T) func(*testing.T) *bun.DB {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), "namespace.db")
	open := func(t *testing.T) *bun.DB {
		t.Helper()

		db, err := sqliterepo.Open(dsn, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		return db
	}

	db := open(t)
	_, err := sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	return open
}

// preparePostgres makes a migrated database on the shared PostgreSQL container and returns its
// opener.
func preparePostgres(t *testing.T) func(*testing.T) *bun.DB {
	t.Helper()

	database := testenv.Postgres(t)
	open := func(t *testing.T) *bun.DB {
		t.Helper()

		return database.Open(t)
	}

	db := open(t)
	_, err := pgrepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	return open
}

func runSlugNamespace(t *testing.T, db *bun.DB, open func(*testing.T) *bun.DB, objects *s3store.Store) {
	t.Helper()

	for _, winner := range []string{"notes", "links", "simultaneous"} {
		t.Run("concurrent_"+winner, func(t *testing.T) {
			testConcurrentCreate(t, db, open, winner)
		})
	}

	t.Run("storage_conflicts", func(t *testing.T) { testStorageConflicts(t, db) })
	t.Run("concurrent_storage", func(t *testing.T) { testConcurrentStorageCreates(t, db, open) })

	for _, winner := range []string{"notes", "links", "files", "simultaneous"} {
		t.Run("concurrent_files_"+winner, func(t *testing.T) {
			testConcurrentFileCreate(t, db, open, objects, winner)
		})
	}

	t.Run("release", func(t *testing.T) { testRelease(t, db) })
	t.Run("release_files", func(t *testing.T) { testFileRelease(t, db, objects) })
	t.Run("expired_file_slug", func(t *testing.T) { testExpiredFileSlug(t, db, objects) })
	t.Run("expired_note_slug", func(t *testing.T) { testExpiredNoteSlug(t, db, objects) })
	t.Run("record_reads", func(t *testing.T) { testRecordReads(t, db, objects) })
	t.Run("rollback", func(t *testing.T) { testRollback(t, db) })
	t.Run("id_change", func(t *testing.T) { testIDChange(t, db) })
	t.Run("legacy_conversion", func(t *testing.T) { testLegacyConversion(t, db) })
	t.Run("conflict_policies", func(t *testing.T) { testConflictPolicies(t, db) })
	t.Run("backfill", func(t *testing.T) { testBackfill(t, db) })
}
