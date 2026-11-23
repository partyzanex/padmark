package testenv

import (
	"database/sql"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Values of the shared PostgreSQL containers. They are for throwaway test containers only.
const (
	// PostgresImage is the image of Postgres. PostgresWithImage selects another one.
	PostgresImage = "postgres:18-alpine"
	// PostgresUser owns each test database. The commands of PostgresDB.Exec use it.
	PostgresUser     = "test"
	postgresPassword = "test"
	postgresAdminDB  = "padmark"
)

// PostgresDB is a new empty database on a PostgreSQL container that the tests of one package
// share. Each database belongs to one caller, so tests on two databases do not see each other.
type PostgresDB struct {
	container *tcpostgres.PostgresContainer
	// DSN is the connection string of the database, with sslmode=disable.
	DSN string
	// Name is the name of the database.
	Name string
}

// Postgres returns a new empty database on the shared container of PostgresImage. The first
// call of the test process starts the container; the reaper stops it (see the package comment).
func Postgres(t *testing.T) *PostgresDB {
	t.Helper()

	return PostgresWithImage(t, PostgresImage)
}

// PostgresWithImage is Postgres on the shared container of image, for a test that needs a
// different PostgreSQL version.
func PostgresWithImage(t *testing.T, image string) *PostgresDB {
	t.Helper()

	postgres, err := tcpostgres.Run(t.Context(), image, testcontainers.WithReuseByName(sharedName(image)),
		tcpostgres.WithDatabase(postgresAdminDB), tcpostgres.WithUsername(PostgresUser),
		tcpostgres.WithPassword(postgresPassword), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err, "start %s", image)

	adminDSN, err := postgres.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)

	database := &PostgresDB{container: postgres, DSN: "", Name: "padmark_test_" + randomSuffix(t)}
	database.Exec(t, "createdb", "-U", PostgresUser, database.Name)

	dsn, err := url.Parse(adminDSN)
	require.NoError(t, err)

	dsn.Path = "/" + database.Name
	database.DSN = dsn.String()

	return database
}

// Open opens the database with the bun PostgreSQL dialect. The test cleanup closes it.
func (d *PostgresDB) Open(t *testing.T) *bun.DB {
	t.Helper()

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(d.DSN))), pgdialect.New())

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return db
}

// Exec runs command in the container and stops the test when the command fails, for example
// pg_dump or pg_restore of a backup test.
func (d *PostgresDB) Exec(t *testing.T, command ...string) {
	t.Helper()

	code, _, err := d.container.Exec(t.Context(), command)
	require.NoError(t, err)
	require.Zero(t, code, "%v", command)
}
