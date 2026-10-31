//go:build integration

package postgres

import (
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// Ports of the SQLite user tests the Postgres suite lacked, plus the races the Postgres
// statements must win without SQLite's single writer.

func (s *ParitySuite) TestUser_SecondAdminRejectedByPartialUniqueIndex() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	s.createParityUser("admin1", true)

	second := s.parityUser("admin2")
	second.IsAdmin = true
	s.Require().ErrorIs(repo.Create(ctx, second), domain.ErrUserExists,
		"idx_users_single_admin must reject a second admin")

	s.Require().NoError(repo.Create(ctx, s.parityUser("regular")), "non-admins are unaffected")
}

func (s *ParitySuite) TestUser_ConcurrentAdminCreateHasSingleWinner() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)

	results := race(func(int) error {
		usr := s.parityUser(uuid.NewString())
		usr.IsAdmin = true

		return repo.Create(ctx, usr)
	})

	created := 0

	for _, result := range results {
		if result == nil {
			created++

			continue
		}

		s.Require().ErrorIs(result, domain.ErrUserExists, "unexpected failure: %v", result)
	}

	s.Equal(1, created, "the first-admin bootstrap race must have exactly one winner")
}

func (s *ParitySuite) TestUser_RedeemInviteUsernameCollisionRollsBackInvite() {
	ctx := s.T().Context()
	users := NewUserRepository(s.db)
	invites := NewInviteRepository(s.db, s.discard)
	creator := s.createParityUser("creator", false)
	s.createParityUser("taken", false)

	tok, err := invites.Issue(ctx, creator.ID)
	s.Require().NoError(err)

	s.Require().ErrorIs(invites.RedeemInvite(ctx, tok, "taken", s.parityUser("taken")), domain.ErrUserExists)

	fresh := s.parityUser("fresh")
	s.Require().NoError(invites.RedeemInvite(ctx, tok, "fresh", fresh), "the invite must not have been burned")

	got, err := users.GetByID(ctx, fresh.ID)
	s.Require().NoError(err)
	s.Equal("fresh", got.Username)
}

func (s *ParitySuite) TestUser_UpdateTOTPCounterPersistsAndBlocksReplay() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	usr := s.createParityUser("totp", false)

	accepted, err := repo.UpdateTOTPCounter(ctx, usr.ID, 100)
	s.Require().NoError(err)
	s.True(accepted)

	for _, counter := range []int64{100, 99, 0, -1} {
		accepted, err = repo.UpdateTOTPCounter(ctx, usr.ID, counter)
		s.Require().NoError(err)
		s.False(accepted, "counter %d must be rejected as a replay", counter)
	}

	accepted, err = repo.UpdateTOTPCounter(ctx, usr.ID, 101)
	s.Require().NoError(err)
	s.True(accepted)

	got, err := repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(int64(101), got.LastTOTPCounter)
}

func (s *ParitySuite) TestUser_UpdateTOTPCounterUnknownUserIsNotAccepted() {
	accepted, err := NewUserRepository(s.db).UpdateTOTPCounter(s.T().Context(), uuid.New(), 1)

	s.Require().NoError(err)
	s.False(accepted)
}

// TestUser_ConcurrentSameTOTPCounterSingleWinner: the same TOTP code submitted concurrently
// must be accepted once. Under READ COMMITTED the losing UPDATEs wait on the row lock and
// re-evaluate "last_totp_counter < ?" against the committed winner.
func (s *ParitySuite) TestUser_ConcurrentSameTOTPCounterSingleWinner() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	usr := s.createParityUser("totp-race", false)

	type outcome struct {
		err      error
		accepted bool
	}

	results := race(func(int) outcome {
		accepted, err := repo.UpdateTOTPCounter(ctx, usr.ID, 4242)

		return outcome{err: err, accepted: accepted}
	})

	accepted := 0

	for _, result := range results {
		s.Require().NoError(result.err)

		if result.accepted {
			accepted++
		}
	}

	s.Equal(1, accepted, "a TOTP code must be accepted exactly once")
}

func (s *ParitySuite) TestUser_UpdateTOTPSecretLeavesCredentialsAlone() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	usr := s.createParityUser("reseal", false)

	s.Require().NoError(repo.UpdateTOTPSecret(ctx, usr.ID, "resealed-secret"))

	got, err := repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal("resealed-secret", got.TOTPSecret)
	s.Equal(usr.PasswordHash, got.PasswordHash)
	s.Equal(usr.KDFSalt, got.KDFSalt)
}

// TestUser_UsernameIsCaseInsensitive pins the behaviour the SQLite tests establish: usernames
// are unique under lower(username), so a case variant is a duplicate, and any spelling finds the
// stored row (the per-username login lockout folds case the same way).
func (s *ParitySuite) TestUser_UsernameIsCaseInsensitive() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	s.createParityUser("alice", false)

	s.Require().ErrorIs(repo.Create(ctx, s.parityUser("Alice")), domain.ErrUserExists)

	got, err := repo.GetByUsername(ctx, "ALICE")
	s.Require().NoError(err)
	s.Equal("alice", got.Username)
}

// TestUser_RevokeClearsCredentialsAndOwnership: revoking a user must cascade-delete every
// credential bound to them (sessions, API tokens, issued invites) and orphan — not delete —
// their notes and links (ON DELETE SET NULL), exactly as the SQLite schema declares.
func (s *ParitySuite) TestUser_RevokeClearsCredentialsAndOwnership() {
	ctx := s.T().Context()
	usr := s.createParityUser("leaver", false)
	now := time.Now()

	s.Require().NoError(NewSessionRepository(s.db, s.discard).Create(ctx, &domain.Session{
		SessionHash: "session-hash", UserID: usr.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),

		UserAgent: "", IP: "",
	}))
	_, err := NewAPITokenRepository(s.db).CreateIfUnderLimit(ctx,
		&domain.APIToken{TokenHash: "token-hash", UserID: usr.ID, CreatedAt: now,
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		}, 10)
	s.Require().NoError(err)
	_, err = NewInviteRepository(s.db, s.discard).Issue(ctx, usr.ID)
	s.Require().NoError(err)

	owned := newNote("owned-note", "t", "c")
	owned.OwnerID = &usr.ID
	s.Require().NoError(NewNoteRepository(s.db).Create(ctx, owned))
	s.Require().NoError(NewLinkRepository(s.db, s.discard).Create(ctx, &domain.Link{
		ID: "owned-link", TargetURL: "ciphertext", CreatedAt: now, OwnerID: &usr.ID,

		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, EditCode: "", Views: 0,
		BurnAfterClick: false,
	}))

	s.Require().NoError(NewUserRepository(s.db).Revoke(ctx, usr.ID))

	for _, table := range []string{"sessions", "api_tokens", "invites"} {
		s.Zero(s.count(table), "%s must be cascade-deleted with the user", table)
	}

	note, err := NewNoteRepository(s.db).Get(ctx, "owned-note")
	s.Require().NoError(err)
	s.Nil(note.OwnerID)

	link, err := NewLinkRepository(s.db, s.discard).GetByID(ctx, "owned-link")
	s.Require().NoError(err)
	s.Nil(link.OwnerID)
}

// TestUser_ConcurrentRevokeOfSameUserSingleWinner: concurrent revocations of one user must
// delete it once; the losers report ErrForbidden (zero affected rows), never an error leak.
func (s *ParitySuite) TestUser_ConcurrentRevokeOfSameUserSingleWinner() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	s.createParityUser("admin", true)
	victim := s.createParityUser("victim", false)

	results := race(func(int) error { return repo.Revoke(ctx, victim.ID) })

	revoked := 0

	for _, result := range results {
		if result == nil {
			revoked++

			continue
		}

		s.Require().ErrorIs(result, domain.ErrForbidden, "unexpected failure: %v", result)
	}

	s.Equal(1, revoked)
	s.Equal(int64(1), s.count("users"), "only the admin may remain")
}

func (s *ParitySuite) TestUser_ConcurrentRevokeNeverRemovesTheAdmin() {
	ctx := s.T().Context()
	repo := NewUserRepository(s.db)
	admin := s.createParityUser("admin", true)

	results := race(func(int) error { return repo.Revoke(ctx, admin.ID) })

	for _, result := range results {
		s.Require().ErrorIs(result, domain.ErrForbidden)
	}

	_, err := repo.GetByID(ctx, admin.ID)
	s.Require().NoError(err, "the last admin must survive concurrent revocations")
}
