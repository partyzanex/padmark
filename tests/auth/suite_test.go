//go:build integration

package integration

import (
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// testArgon2Params is the minimal argon2id cost (testutil.Argon2Params): these end-to-end flow
// tests exercise real hash/verify logic, and the production cost is covered on its own in
// internal/infra/crypto's tests.
//
//nolint:gochecknoglobals // test helper
var testArgon2Params = testutil.Argon2Params()

// AuthSuite holds the account repositories of one database for the flow suites. The repository
// contract itself, on SQLite and PostgreSQL, is in tests/storage.
type AuthSuite struct {
	suite.Suite

	Users    auth.UserStore
	Invites  auth.InviteStore
	Sessions auth.SessionStore
}
