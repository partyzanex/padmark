//go:build integration

package postgres

import (
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// Single-use reads on PostgreSQL: burn-after-reading notes, burn-after-click links, reveal
// tokens and the burn grace timer. SQLite gets atomicity from its single writer; Postgres relies
// on DELETE/UPDATE ... RETURNING re-checking the WHERE clause after the row lock, so these pin
// "exactly one winner" under real concurrency.

func singleWinner[T any](results []T, won func(T) bool) int {
	winners := 0

	for _, result := range results {
		if won(result) {
			winners++
		}
	}

	return winners
}

type consumeOutcome struct {
	err error
	ok  bool
}

func (s *ParitySuite) requireOnlyNotFound(results []consumeOutcome) {
	for _, result := range results {
		if result.err != nil {
			s.Require().ErrorIs(result.err, domain.ErrNotFound, "unexpected failure: %v", result.err)
		}
	}
}

func (s *ParitySuite) TestNote_ConcurrentConsumeSingleWinner() {
	ctx := s.T().Context()
	repo := NewNoteRepository(s.db)
	burn := newNote("burn-race", "t", "secret")
	burn.BurnAfterReading = true
	s.Require().NoError(repo.Create(ctx, burn))

	results := race(func(int) consumeOutcome {
		got, err := repo.Consume(ctx, "burn-race", domain.PrivacyPublic)

		return consumeOutcome{err: err, ok: err == nil && got.Content == "secret"}
	})

	s.requireOnlyNotFound(results)
	s.Equal(1, singleWinner(results, func(r consumeOutcome) bool { return r.ok }),
		"a burn-after-reading note must be delivered exactly once")
	s.Zero(s.count("notes"))
}

func (s *ParitySuite) TestNote_SetBurnExpiryStartsTimerOnce() {
	ctx := s.T().Context()
	repo := NewNoteRepository(s.db)
	burn := newNote("burn-ttl", "t", "c")
	burn.BurnAfterReading = true
	burn.BurnTTL = 60
	s.Require().NoError(repo.Create(ctx, burn))

	expiresAt := time.Now().Add(time.Minute).Truncate(time.Microsecond)

	got, err := repo.SetBurnExpiry(ctx, "burn-ttl", expiresAt, domain.PrivacyPublic)
	s.Require().NoError(err)
	s.False(got.BurnAfterReading)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expiresAt.Equal(*got.ExpiresAt))

	_, err = repo.SetBurnExpiry(ctx, "burn-ttl", expiresAt.Add(time.Hour), domain.PrivacyPublic)
	s.Require().ErrorIs(err, domain.ErrNotFound, "the grace timer must not be restartable")

	_, err = repo.Consume(ctx, "burn-ttl", domain.PrivacyPublic)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a note inside its grace period is not consumable")
}

func (s *ParitySuite) TestNote_SetBurnExpiryIgnoresNonBurnAndUnknownNotes() {
	ctx := s.T().Context()
	repo := NewNoteRepository(s.db)
	s.Require().NoError(repo.Create(ctx, newNote("plain", "t", "c")))

	_, err := repo.SetBurnExpiry(ctx, "plain", time.Now().Add(time.Minute), domain.PrivacyPublic)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = repo.SetBurnExpiry(ctx, "missing", time.Now().Add(time.Minute), domain.PrivacyPublic)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	got, err := repo.Get(ctx, "plain")
	s.Require().NoError(err)
	s.Nil(got.ExpiresAt, "a non-burn note must not gain an expiry")
}

func (s *ParitySuite) TestNote_ConcurrentSetBurnExpirySingleWinner() {
	ctx := s.T().Context()
	repo := NewNoteRepository(s.db)
	burn := newNote("ttl-race", "t", "c")
	burn.BurnAfterReading = true
	burn.BurnTTL = 60
	s.Require().NoError(repo.Create(ctx, burn))

	results := race(func(attempt int) consumeOutcome {
		expiresAt := time.Now().Add(time.Duration(attempt+1) * time.Minute)
		_, err := repo.SetBurnExpiry(ctx, "ttl-race", expiresAt, domain.PrivacyPublic)

		return consumeOutcome{err: err, ok: err == nil}
	})

	s.requireOnlyNotFound(results)
	s.Equal(1, singleWinner(results, func(r consumeOutcome) bool { return r.ok }),
		"the first read must start the grace timer exactly once")
}

func (s *ParitySuite) TestLink_ConcurrentConsumeSingleWinner() {
	ctx := s.T().Context()
	repo := NewLinkRepository(s.db, s.discard)
	s.Require().NoError(repo.Create(ctx, &domain.Link{
		ID: "click-race", TargetURL: "ciphertext", CreatedAt: time.Now(), BurnAfterClick: true,

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", Views: 0,
	}))

	results := race(func(int) consumeOutcome {
		got, err := repo.Consume(ctx, "click-race")

		return consumeOutcome{err: err, ok: err == nil && got.TargetURL == "ciphertext"}
	})

	s.requireOnlyNotFound(results)
	s.Equal(1, singleWinner(results, func(r consumeOutcome) bool { return r.ok }),
		"a burn-after-click link must redirect exactly once")
}

func (s *ParitySuite) TestReveal_ConcurrentConsumeSingleWinner() {
	ctx := s.T().Context()
	repo := NewRevealRepository(s.db, s.discard)

	tok, err := repo.Issue(ctx, "note-id")
	s.Require().NoError(err)

	results := race(func(int) bool { return repo.Consume(ctx, tok, "note-id") })

	s.Equal(1, singleWinner(results, func(ok bool) bool { return ok }),
		"a reveal token must be consumable exactly once")
}

// TestExpiry_ComparesInstantsNotWallClocks: TIMESTAMPTZ columns compare instants, so a timestamp
// written in a far-east or far-west zone must expire at the same moment as its UTC equivalent.
// SQLite gets the same result because bun normalises every time.Time to UTC text.
func (s *ParitySuite) TestExpiry_ComparesInstantsNotWallClocks() {
	ctx := s.T().Context()
	east := time.FixedZone("UTC+14", 14*60*60)
	west := time.FixedZone("UTC-12", -12*60*60)
	notes := NewNoteRepository(s.db)

	past := time.Now().Add(-time.Minute).In(east)
	expired := newNote("expired-east", "t", "c")
	expired.ExpiresAt = &past
	s.Require().NoError(notes.Create(ctx, expired))

	future := time.Now().Add(time.Hour).In(west)
	live := newNote("live-west", "t", "c")
	live.ExpiresAt = &future
	s.Require().NoError(notes.Create(ctx, live))

	_, err := notes.Consume(ctx, "expired-east", domain.PrivacyPublic)
	s.Require().NoError(err, "a past instant in UTC+14 is expired")

	_, err = notes.Consume(ctx, "live-west", domain.PrivacyPublic)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a future instant in UTC-12 is not expired")

	got, err := notes.Get(ctx, "live-west")
	s.Require().NoError(err)
	s.True(future.Truncate(time.Microsecond).Equal(*got.ExpiresAt), "the instant must round-trip")

	sessions := NewSessionRepository(s.db, s.discard)
	usr := s.createParityUser("zones", false)
	s.Require().NoError(sessions.Create(ctx, &domain.Session{
		SessionHash: "east", UserID: usr.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Second).In(east),

		UserAgent: "", IP: "",
	}))
	s.Require().NoError(sessions.Create(ctx, &domain.Session{
		SessionHash: "west", UserID: usr.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour).In(west),

		UserAgent: "", IP: "",
	}))

	_, err = sessions.Get(ctx, "east")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)

	_, err = sessions.Get(ctx, "west")
	s.Require().NoError(err)
}
