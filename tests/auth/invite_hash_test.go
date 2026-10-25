//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

func (s *SQLiteAuthSuite) TestInviteHashAtRest() {
	assertInviteHashAtRest(s.T(), s.db, &s.AuthSuite)
}

func (s *PostgresAuthSuite) TestInviteHashAtRest() {
	assertInviteHashAtRest(s.T(), s.db, &s.AuthSuite)
}

func assertInviteHashAtRest(t *testing.T, db *bun.DB, suite *AuthSuite) {
	t.Helper()

	issuer := suite.newAdminUser("hash-issuer")
	require.NoError(t, suite.Users.Create(t.Context(), issuer))
	plain, err := suite.Invites.Issue(t.Context(), issuer.ID)
	require.NoError(t, err)

	var stored string

	require.NoError(t, db.NewSelect().Table("invites").Column("token_hash").Scan(t.Context(), &stored))

	digest := sha256.Sum256([]byte(plain))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(digest[:]), stored)
	require.NotEqual(t, plain, stored)
	_, err = suite.Invites.Consume(t.Context(), stored, "intruder")
	require.ErrorIs(t, err, domain.ErrNotFound)
	invite, err := suite.Invites.Consume(t.Context(), plain, "member")
	require.NoError(t, err)
	require.Equal(t, stored, invite.TokenHash)
	_, err = suite.Invites.Consume(t.Context(), plain, "replay")
	require.ErrorIs(t, err, domain.ErrInviteUsed)
}
