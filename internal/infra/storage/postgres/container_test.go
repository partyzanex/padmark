//go:build integration

package postgres

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Values of the PostgreSQL container that the repository suites share. They are for the
// throwaway test container only.
const (
	testImage    = "postgres:18-alpine"
	testUser     = "test"
	testPassword = "test"
	testAdminDB  = "padmark"
	// testNameBytes is the number of random bytes in the name of a suite database.
	testNameBytes = 8
)

// newTestDB opens a new empty database on the PostgreSQL container of this test binary, applies
// the migrations and returns it. Each suite gets its own database, so the suites stay isolated
// as with one container for each suite. The suite closes the database.
//
// This package cannot import tests/internal/testenv (Go internal rule), so it repeats the small
// part that it needs. The container name has the process ID, and testcontainers reuses the
// container of that name: the suites of this binary share one container. The suites start one
// after another, so no two first calls run at the same time. The Ryuk reaper of testcontainers
// stops the container after the test process ends. DSNErrorLeakSuite keeps its own container:
// it needs known credentials.
func newTestDB(t *testing.T) *bun.DB {
	t.Helper()

	container, err := tcpostgres.Run(t.Context(), testImage,
		testcontainers.WithReuseByName("padmark-testenv-repositories-"+strconv.Itoa(os.Getpid())),
		tcpostgres.WithDatabase(testAdminDB), tcpostgres.WithUsername(testUser),
		tcpostgres.WithPassword(testPassword), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)

	adminDSN, err := container.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)

	random := make([]byte, testNameBytes)
	_, err = rand.Read(random)
	require.NoError(t, err)

	name := "padmark_test_" + hex.EncodeToString(random)
	code, _, err := container.Exec(t.Context(), []string{"createdb", "-U", testUser, name})
	require.NoError(t, err)
	require.Zero(t, code)

	dsn, err := url.Parse(adminDSN)
	require.NoError(t, err)

	dsn.Path = "/" + name

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn.String()))), pgdialect.New())
	require.NoError(t, db.PingContext(t.Context()))

	_, err = Migrate(t.Context(), db)
	require.NoError(t, err)

	return db
}
