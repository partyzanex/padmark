//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/server"
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
)

// DSNErrorLeakSuite checks, against a real PostgreSQL server, that connection failures caused
// by a reachable server rejecting the DSN (wrong password, wrong user, missing database) never
// echo the DSN password: not through the driver error, not through the error the serve/migrate
// commands return (cmd/padmark-server logs it verbatim), and not through anything they log.
// The parse-time panics for malformed DSNs are covered by internal/infra/server's
// dsn_error_leak_test.go, which needs no server.
type DSNErrorLeakSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	baseDSN   *url.URL
	password  string
	keyring   string
}

// leakSuitePassword is assembled at runtime so no literal looks like a hardcoded credential.
func leakSuitePassword(prefix string) string {
	return strings.Join([]string{prefix, "Sup3r", "S3cret", "PW"}, "-")
}

func (s *DSNErrorLeakSuite) SetupSuite() {
	ctx := s.T().Context()
	s.password = leakSuitePassword("Real")

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark"),
		tcpostgres.WithUsername("padmark"),
		tcpostgres.WithPassword(s.password),
		tcpostgres.BasicWaitStrategies(),
	)
	s.Require().NoError(err)

	s.container = container

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	s.baseDSN, err = url.Parse(dsn)
	s.Require().NoError(err)

	s.keyring = filepath.Join(s.T().TempDir(), "keyring.json")
	s.Require().NoError(crypto.WriteContentKeyring(s.keyring, &crypto.ContentKeyring{
		Active: "leak-test", Keys: map[string][]byte{"leak-test": bytes.Repeat([]byte{7}, 32)},
	}))
}

func (s *DSNErrorLeakSuite) TearDownSuite() {
	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func TestDSNErrorLeakSuite(t *testing.T) {
	suite.Run(t, new(DSNErrorLeakSuite))
}

type rejectedDSN struct {
	name   string
	dsn    string
	secret string
}

func (s *DSNErrorLeakSuite) rejectedDSNs() []rejectedDSN {
	wrongPassword := leakSuitePassword("Wrong")

	withCreds := func(user, password, database string) string {
		dsn := *s.baseDSN
		dsn.User = url.UserPassword(user, password)
		dsn.Path = "/" + database

		return dsn.String()
	}

	return []rejectedDSN{
		{name: "wrong password", dsn: withCreds("padmark", wrongPassword, "padmark"), secret: wrongPassword},
		{name: "unknown user", dsn: withCreds("nobody", s.password, "padmark"), secret: s.password},
		{name: "missing database", dsn: withCreds("padmark", s.password, "no_such_db"), secret: s.password},
	}
}

// openLikeServer mirrors server.openPostgresDB + openDB + initPostgresStorage: connector from
// the DSN, ping, then goose migrations.
func openLikeServer(ctx context.Context, dsn string) error {
	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	defer db.Close()

	err := db.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("db ping: %w", err)
	}

	_, err = postgres.Migrate(ctx, db)
	if err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}

	return nil
}

func (s *DSNErrorLeakSuite) TestDriverPingAndMigrate_RejectedDSNNeverEchoesPassword() {
	for _, testCase := range s.rejectedDSNs() {
		s.Run(testCase.name, func() {
			err := openLikeServer(s.T().Context(), testCase.dsn)

			s.Require().Error(err)
			s.NotContains(err.Error(), testCase.secret)
			s.NotContains(err.Error(), s.password)
		})
	}
}

// captureOutput swaps os.Stderr (the server logger binds it at construction) and the default
// slog logger for the duration of body and returns everything written to either.
func (s *DSNErrorLeakSuite) captureOutput(body func()) string {
	s.T().Helper()

	reader, writer, err := os.Pipe()
	s.Require().NoError(err)

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

	s.Require().NoError(writer.Close())

	return buf.String() + string(<-done)
}

func (s *DSNErrorLeakSuite) TestServeAndMigrateCommands_RejectedDSNNeverEchoesPassword() {
	for _, testCase := range s.rejectedDSNs() {
		for _, command := range []string{"serve", "migrate"} {
			s.Run(command+"/"+testCase.name, func() {
				args := []string{"padmark", command, "--storage", "postgres", "--dsn", testCase.dsn}
				if command == "serve" {
					args = append(args, "--content-keyring-file", s.keyring, "--addr", "127.0.0.1:0")
				}

				var runErr error

				output := s.captureOutput(func() {
					runErr = server.NewApp().Run(context.WithoutCancel(s.T().Context()), args)
				})

				s.Require().Error(runErr)
				s.NotContains(runErr.Error(), testCase.secret, "the error cmd/padmark-server logs")
				s.NotContains(output, testCase.secret, "stderr and slog output")
			})
		}
	}
}

// TestMigrateCommand_SuccessLogsNeverEchoPassword: the happy path logs too; it must not print
// the DSN either.
func (s *DSNErrorLeakSuite) TestMigrateCommand_SuccessLogsNeverEchoPassword() {
	var runErr error

	output := s.captureOutput(func() {
		runErr = server.NewApp().Run(context.WithoutCancel(s.T().Context()),
			[]string{"padmark", "migrate", "--storage", "postgres", "--dsn", s.baseDSN.String()})
	})

	s.Require().NoError(runErr)
	s.Contains(output, "migrations")
	s.NotContains(output, s.password)
}
