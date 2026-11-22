//go:build integration

package storage

import (
	"context"
	"time"
)

// RevealSuite is the contract of the reveal token repository.
type RevealSuite struct {
	repoSuite
}

func (s *RevealSuite) TestConsume_OnceOnly() {
	ctx := s.T().Context()
	instant := now()
	s.Require().NoError(s.be.reveals.Create(ctx, "hash-1", "note-1", instant.Add(time.Minute)))

	consumed, err := s.be.reveals.Consume(ctx, "hash-1", "note-1", instant)
	s.Require().NoError(err)
	s.True(consumed)

	consumed, err = s.be.reveals.Consume(ctx, "hash-1", "note-1", instant)
	s.Require().NoError(err)
	s.False(consumed, "a used token is refused")
}

func (s *RevealSuite) TestConsume_WrongNote_KeepsToken() {
	ctx := s.T().Context()
	instant := now()
	s.Require().NoError(s.be.reveals.Create(ctx, "hash-1", "note-1", instant.Add(time.Minute)))

	consumed, err := s.be.reveals.Consume(ctx, "hash-1", "note-2", instant)
	s.Require().NoError(err)
	s.False(consumed)

	consumed, err = s.be.reveals.Consume(ctx, "hash-1", "note-1", instant)
	s.Require().NoError(err)
	s.True(consumed, "the owner can still use the token")
}

func (s *RevealSuite) TestConsume_Unknown_ReturnsFalseWithoutError() {
	consumed, err := s.be.reveals.Consume(s.T().Context(), "unknown", "note-1", now())
	s.Require().NoError(err)
	s.False(consumed)
}

// TestConsume_ExpiryBoundary pins the same boundary on both dialects: a token is valid only
// while now is before expires_at.
func (s *RevealSuite) TestConsume_ExpiryBoundary() {
	ctx := s.T().Context()
	expires := now().Add(time.Minute)
	s.Require().NoError(s.be.reveals.Create(ctx, "hash-1", "note-1", expires))

	consumed, err := s.be.reveals.Consume(ctx, "hash-1", "note-1", expires)
	s.Require().NoError(err)
	s.False(consumed, "a token is expired at expires_at")

	consumed, err = s.be.reveals.Consume(ctx, "hash-1", "note-1", expires.Add(-time.Microsecond))
	s.Require().NoError(err)
	s.True(consumed)
}

// TestConsume_Concurrent_SingleWinner pins S4: SQLite and PostgreSQL both use one conditional
// UPDATE, so only one of the concurrent requests reveals the note.
func (s *RevealSuite) TestConsume_Concurrent_SingleWinner() {
	ctx := s.T().Context()
	instant := now()
	s.Require().NoError(s.be.reveals.Create(ctx, "hash-1", "note-1", instant.Add(time.Minute)))

	results := race(func(int) bool {
		consumed, err := s.be.reveals.Consume(context.WithoutCancel(ctx), "hash-1", "note-1", instant)
		s.NoError(err)

		return consumed
	})

	s.Equal(1, countTrue(results))
}

// TestConsume_StoreFailure_ReturnsError pins C4: a database failure is an error, not a
// wrong token.
func (s *RevealSuite) TestConsume_StoreFailure_ReturnsError() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	consumed, err := s.be.reveals.Consume(ctx, "hash-1", "note-1", now())
	s.Require().Error(err)
	s.False(consumed)
}

func (s *RevealSuite) TestDeleteStale_RemovesUsedAndExpired() {
	ctx := s.T().Context()
	instant := now()
	s.Require().NoError(s.be.reveals.Create(ctx, "live", "note-1", instant.Add(time.Minute)))
	s.Require().NoError(s.be.reveals.Create(ctx, "expired", "note-1", instant.Add(-time.Minute)))
	s.Require().NoError(s.be.reveals.Create(ctx, "used", "note-1", instant.Add(time.Minute)))
	consumed, err := s.be.reveals.Consume(ctx, "used", "note-1", instant)
	s.Require().NoError(err)
	s.Require().True(consumed)

	s.Require().NoError(s.be.reveals.DeleteStale(ctx, instant))

	var hashes []string

	s.Require().NoError(s.be.db.NewSelect().Table("reveal_tokens").Column("token_hash").Scan(ctx, &hashes))
	s.Equal([]string{"live"}, hashes)
}
