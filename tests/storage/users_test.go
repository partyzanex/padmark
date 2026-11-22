//go:build integration

package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// UserSuite is the contract of the user repository.
type UserSuite struct {
	repoSuite
}

func (s *UserSuite) TestCreate_GetByUsername_RoundTrip() {
	ctx := s.T().Context()
	usr := s.createUser("alice", false)

	got, err := s.be.users.GetByUsername(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal("alice", got.Username)
	s.Equal(usr.TOTPSecret, got.TOTPSecret)
	s.Equal(usr.PasswordHash, got.PasswordHash)
	s.Equal(usr.KDFSalt, got.KDFSalt)
	s.False(got.IsAdmin)
	s.Nil(got.LastLoginAt)
}

func (s *UserSuite) TestCreate_DuplicateUsername_ReturnsErrUserExists() {
	s.createUser("bob", false)

	err := s.be.users.Create(s.T().Context(), newUser("bob", false))
	s.Require().ErrorIs(err, domain.ErrUserExists)
}

func (s *UserSuite) TestUsername_ASCIICase_IsOneUser() {
	ctx := s.T().Context()
	usr := s.createUser("Carol", false)

	got, err := s.be.users.GetByUsername(ctx, "cAROL")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal("Carol", got.Username, "the stored spelling stays")

	s.Require().ErrorIs(s.be.users.Create(ctx, newUser("CAROL", false)), domain.ErrUserExists)
}

// TestUsername_CyrillicCase_IsOneUser pins the S4 username finding: SQLite lower() folds
// ASCII letters only, so on SQLite "Ёжик" and "ёжик" are two users. The fix needs a decision
// of the owner about stored rows (see the report), so the case is skipped on SQLite.
func (s *UserSuite) TestUsername_CyrillicCase_IsOneUser() {
	if s.be.dialect == dialectSQLite {
		s.T().Skip("S4: SQLite lower() is ASCII-only; the fix needs a data migration decision")
	}

	ctx := s.T().Context()
	usr := s.createUser("Ёжик", false)

	got, err := s.be.users.GetByUsername(ctx, "ёЖИК")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)

	s.Require().ErrorIs(s.be.users.Create(ctx, newUser("ЁЖИК", false)), domain.ErrUserExists)
}

func (s *UserSuite) TestGetByUsername_Unknown_ReturnsErrNotFound() {
	_, err := s.be.users.GetByUsername(s.T().Context(), "nobody")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *UserSuite) TestGetByID() {
	ctx := s.T().Context()
	usr := s.createUser("dave", false)

	got, err := s.be.users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal("dave", got.Username)

	_, err = s.be.users.GetByID(ctx, uuid.New())
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *UserSuite) TestList_OrderedByCreatedAt() {
	ctx := s.T().Context()
	first := newUser("first", false)
	second := newUser("second", false)
	second.CreatedAt = first.CreatedAt.Add(time.Second)

	s.Require().NoError(s.be.users.Create(ctx, second))
	s.Require().NoError(s.be.users.Create(ctx, first))

	users, err := s.be.users.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(users, 2)
	s.Equal("first", users[0].Username)
	s.Equal("second", users[1].Username)
}

func (s *UserSuite) TestSecondAdmin_ReturnsErrUserExists() {
	s.createUser("admin", true)

	err := s.be.users.Create(s.T().Context(), newUser("admin2", true))
	s.Require().ErrorIs(err, domain.ErrUserExists, "idx_users_single_admin allows one admin")
}

func (s *UserSuite) TestUpdateLastLogin() {
	ctx := s.T().Context()
	usr := s.createUser("erin", false)
	instant := now()

	s.Require().NoError(s.be.users.UpdateLastLogin(ctx, usr.ID, instant))

	got, err := s.be.users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastLoginAt)
	s.True(instant.Equal(*got.LastLoginAt))
}

// TestUpdateLastLogin_UnknownUser_ReturnsErrNotFound pins C4: a write that changes no row is
// not a success.
func (s *UserSuite) TestUpdateLastLogin_UnknownUser_ReturnsErrNotFound() {
	err := s.be.users.UpdateLastLogin(s.T().Context(), uuid.New(), now())
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *UserSuite) TestUpdateTOTPCounter_AdvancesAndBlocksReplay() {
	ctx := s.T().Context()
	usr := s.createUser("frank", false)

	accepted, err := s.be.users.UpdateTOTPCounter(ctx, usr.ID, 10)
	s.Require().NoError(err)
	s.True(accepted)

	accepted, err = s.be.users.UpdateTOTPCounter(ctx, usr.ID, 10)
	s.Require().NoError(err)
	s.False(accepted, "the same counter is a replay")

	accepted, err = s.be.users.UpdateTOTPCounter(ctx, usr.ID, 9)
	s.Require().NoError(err)
	s.False(accepted, "an older counter is a replay")

	accepted, err = s.be.users.UpdateTOTPCounter(ctx, uuid.New(), 11)
	s.Require().NoError(err)
	s.False(accepted, "an unknown user accepts nothing")
}

func (s *UserSuite) TestUpdateTOTPCounter_ConcurrentSameCounter_SingleWinner() {
	ctx := s.T().Context()
	usr := s.createUser("grace", false)

	results := race(func(int) bool {
		ok, err := s.be.users.UpdateTOTPCounter(ctx, usr.ID, 42)
		s.NoError(err)

		return ok
	})

	s.Equal(1, countTrue(results))
}

func (s *UserSuite) TestUpdateTOTPSecret_IsCompareAndSet() {
	ctx := s.T().Context()
	usr := s.createUser("heidi", false)

	updated, err := s.be.users.UpdateTOTPSecret(ctx, usr.ID, "stale-seal", "new-seal")
	s.Require().NoError(err)
	s.False(updated, "a stale expected seal writes nothing")

	updated, err = s.be.users.UpdateTOTPSecret(ctx, usr.ID, usr.TOTPSecret, "new-seal")
	s.Require().NoError(err)
	s.True(updated)

	got, err := s.be.users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal("new-seal", got.TOTPSecret)
	s.Equal(usr.PasswordHash, got.PasswordHash, "the password hash stays")
	s.Equal(usr.KDFSalt, got.KDFSalt, "the KDF salt stays")
}

func (s *UserSuite) TestChangePassword_ReplacesCredentialsAndAccess() {
	ctx := s.T().Context()
	usr := s.createUser("ivan", false)
	other := s.createUser("judy", false)
	expires := now().Add(time.Hour)

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("old-session", usr.ID, expires)))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("other-session", other.ID, expires)))
	ok, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("old-token", usr.ID, nil), 5)
	s.Require().NoError(err)
	s.Require().True(ok)

	s.Require().NoError(s.be.users.ChangePassword(ctx, newSession("new-session", usr.ID, expires),
		"new-hash", []byte("new-salt-16bytes"), "new-seal"))

	got, err := s.be.users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal("new-hash", got.PasswordHash)
	s.Equal([]byte("new-salt-16bytes"), got.KDFSalt)
	s.Equal("new-seal", got.TOTPSecret)

	_, err = s.be.sessions.Get(ctx, "old-session")
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "the old session is gone")
	_, err = s.be.sessions.Get(ctx, "new-session")
	s.Require().NoError(err, "the replacement session exists")
	_, err = s.be.sessions.Get(ctx, "other-session")
	s.Require().NoError(err, "sessions of other users stay")
	_, err = s.be.tokens.GetByHash(ctx, "old-token")
	s.Require().ErrorIs(err, domain.ErrNotFound, "the API tokens of the user are gone")
}

func (s *UserSuite) TestChangePassword_UnknownUser_ChangesNothing() {
	ctx := s.T().Context()
	ghost := uuid.New()

	err := s.be.users.ChangePassword(ctx, newSession("ghost-session", ghost, now().Add(time.Hour)),
		"new-hash", []byte("new-salt-16bytes"), "new-seal")
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = s.be.sessions.Get(ctx, "ghost-session")
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "the transaction stored no session")
}

func (s *UserSuite) TestRevoke_NonAdmin_RemovesUserAndSessions() {
	ctx := s.T().Context()
	usr := s.createUser("mallory", false)
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("mallory-session", usr.ID, now().Add(time.Hour))))

	s.Require().NoError(s.be.users.Revoke(ctx, usr.ID))

	_, err := s.be.users.GetByID(ctx, usr.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound)
	_, err = s.be.sessions.Get(ctx, "mallory-session")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
}

// TestRevoke_CascadesCredentialsAndOrphansRecords pins the foreign keys of both schemas: the
// sessions, API tokens and invites of the user go with the user; the notes and links stay
// without an owner.
func (s *UserSuite) TestRevoke_CascadesCredentialsAndOrphansRecords() {
	ctx := s.T().Context()
	usr := s.createUser("leaver", false)

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("leaver-session", usr.ID, now().Add(time.Hour))))
	ok, err := s.be.tokens.CreateIfUnderLimit(ctx, newAPIToken("leaver-token", usr.ID, nil), 5)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().NoError(s.be.invites.Create(ctx, newInvite("leaver-invite", usr.ID, now().Add(time.Hour))))

	note := newNote("owned-note", "edit-hash", nil)
	note.OwnerID = &usr.ID
	s.Require().NoError(s.be.notes.Create(ctx, note))

	link := newLink("owned-link", nil)
	link.OwnerID = &usr.ID
	s.Require().NoError(s.be.links.Create(ctx, link))

	s.Require().NoError(s.be.users.Revoke(ctx, usr.ID))

	for _, table := range []string{"sessions", "api_tokens", "invites"} {
		count, countErr := s.be.db.NewSelect().Table(table).Count(ctx)
		s.Require().NoError(countErr)
		s.Zero(count, "%s rows go with the user", table)
	}

	gotNote, err := s.be.notes.Get(ctx, "owned-note")
	s.Require().NoError(err)
	s.Nil(gotNote.OwnerID)

	gotLink, err := s.be.links.GetByID(ctx, "owned-link")
	s.Require().NoError(err)
	s.Nil(gotLink.OwnerID)
}

// TestRevoke_Admin_ReturnsErrForbidden pins Y5: the schema allows one admin, so Revoke never
// deletes an admin.
func (s *UserSuite) TestRevoke_Admin_ReturnsErrForbidden() {
	ctx := s.T().Context()
	admin := s.createUser("root", true)

	s.Require().ErrorIs(s.be.users.Revoke(ctx, admin.ID), domain.ErrForbidden)

	_, err := s.be.users.GetByID(ctx, admin.ID)
	s.NoError(err, "the admin stays")
}

func (s *UserSuite) TestRevoke_Unknown_ReturnsErrForbidden() {
	s.Require().ErrorIs(s.be.users.Revoke(s.T().Context(), uuid.New()), domain.ErrForbidden)
}

func (s *UserSuite) TestRevoke_ConcurrentSameUser_SingleWinner() {
	ctx := s.T().Context()
	usr := s.createUser("trent", false)

	results := race(func(int) error {
		return s.be.users.Revoke(context.WithoutCancel(ctx), usr.ID)
	})

	won := 0

	for _, err := range results {
		if err == nil {
			won++

			continue
		}

		s.Require().ErrorIs(err, domain.ErrForbidden)
	}

	s.Equal(1, won)
}
