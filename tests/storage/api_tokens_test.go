//go:build integration

package storage

import (
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// APITokenSuite is the contract of the API token repository.
type APITokenSuite struct {
	repoSuite
}

func (s *APITokenSuite) TestCreate_GetByHash_RoundTrip() {
	ctx := s.T().Context()
	usr := s.createUser("alice", false)
	expires := now().Add(time.Hour)

	created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("parent-hash", usr.ID, nil), 5)
	s.Require().NoError(err)
	s.Require().True(created)

	token := newAPIToken("hash-1", usr.ID, &expires)
	token.IssuedBy = new("parent-hash")

	created, err = s.be.tokens.CreateIfUnderLimit(ctx, token, 5)
	s.Require().NoError(err)
	s.Require().True(created)

	got, err := s.be.tokens.GetByHash(ctx, "hash-1")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.UserID)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt))
	s.Require().NotNil(got.IssuedBy)
	s.Equal("parent-hash", *got.IssuedBy)
	s.Nil(got.LastUsedAt)

	_, err = s.be.tokens.GetByHash(ctx, "unknown")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestCreateIfUnderLimit_CountsOnlyLiveTokens pins that an expired token does not use up the
// limit, and that the limit then refuses without an error.
func (s *APITokenSuite) TestCreateIfUnderLimit_CountsOnlyLiveTokens() {
	ctx := s.T().Context()
	usr := s.createUser("bob", false)
	expired := now().Add(-time.Minute)

	created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("expired", usr.ID, &expired), 1)
	s.Require().NoError(err)
	s.Require().True(created)

	created, err = s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("live", usr.ID, nil), 1)
	s.Require().NoError(err)
	s.True(created, "the expired token does not count")

	created, err = s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("over", usr.ID, nil), 1)
	s.Require().NoError(err)
	s.False(created, "the limit is reached")
}

func (s *APITokenSuite) TestCreateIfUnderLimit_Concurrent_NeverExceedsLimit() {
	ctx := s.T().Context()
	usr := s.createUser("carol", false)

	const limit = 3

	results := race(func(attempt int) bool {
		created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken(uuid.NewString(), usr.ID, nil), limit)
		s.NoError(err, "attempt %d", attempt)

		return created
	})

	s.Equal(limit, countTrue(results))
}

func (s *APITokenSuite) TestList_RevokeByHash() {
	ctx := s.T().Context()
	usr := s.createUser("dave", false)

	for _, hash := range []string{"one", "two"} {
		created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken(hash, usr.ID, nil), 5)
		s.Require().NoError(err)
		s.Require().True(created)
	}

	tokens, err := s.be.tokens.List(ctx)
	s.Require().NoError(err)
	s.Len(tokens, 2)

	s.Require().NoError(s.be.tokens.RevokeByHash(ctx, "one"))
	s.Require().ErrorIs(s.be.tokens.RevokeByHash(ctx, "one"), domain.ErrNotFound)

	tokens, err = s.be.tokens.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(tokens, 1)
	s.Equal("two", tokens[0].TokenHash)
}

func (s *APITokenSuite) TestUpdateLastUsed() {
	ctx := s.T().Context()
	usr := s.createUser("erin", false)
	created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("used", usr.ID, nil), 5)
	s.Require().NoError(err)
	s.Require().True(created)

	instant := now()
	s.Require().NoError(s.be.tokens.UpdateLastUsed(ctx, "used", instant))

	got, err := s.be.tokens.GetByHash(ctx, "used")
	s.Require().NoError(err)
	s.Require().NotNil(got.LastUsedAt)
	s.True(instant.Equal(*got.LastUsedAt))
}

func (s *APITokenSuite) TestDeleteByUserID_KeepsOtherUsers() {
	ctx := s.T().Context()
	owner := s.createUser("frank", false)
	other := s.createUser("grace", false)

	for hash, userID := range map[string]uuid.UUID{"owner": owner.ID, "other": other.ID} {
		created, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken(hash, userID, nil), 5)
		s.Require().NoError(err)
		s.Require().True(created)
	}

	s.Require().NoError(s.be.tokens.DeleteByUserID(ctx, owner.ID))
	s.Require().NoError(s.be.tokens.DeleteByUserID(ctx, owner.ID), "a user without tokens is not an error")

	_, err := s.be.tokens.GetByHash(ctx, "owner")
	s.Require().ErrorIs(err, domain.ErrNotFound)
	_, err = s.be.tokens.GetByHash(ctx, "other")
	s.NoError(err)
}
