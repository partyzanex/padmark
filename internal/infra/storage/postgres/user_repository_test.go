package postgres

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

type UserRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *UserRepository
}

func (s *UserRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_user_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	s.Require().NoError(err)

	s.container = container

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	s.db = bun.NewDB(sqldb, pgdialect.New())
	s.Require().NoError(s.db.PingContext(ctx))

	_, err = Migrate(ctx, s.db)
	s.Require().NoError(err)

	s.repo = NewUserRepository(s.db)
}

func (s *UserRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *UserRepositoryTestSuite) SetupTest() {
	ctx := s.T().Context()

	_, err := s.db.NewTruncateTable().
		TableExpr("sessions").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)

	_, err = s.db.NewTruncateTable().
		TableExpr("users").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)
}

func TestUserRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(UserRepositoryTestSuite))
}

func (s *UserRepositoryTestSuite) newUser(username string) *domain.User {
	return &domain.User{
		ID:           uuid.New(),
		Username:     username,
		TOTPSecret:   "totp-secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		IsAdmin:      false,
		CreatedAt:    time.Now().Truncate(time.Second),
	}
}

// ── Create ──

func (s *UserRepositoryTestSuite) TestCreate_GetByUsername_RoundTrip() {
	ctx := s.T().Context()
	usr := s.newUser("alice")

	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByUsername(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal(usr.Username, got.Username)
	s.Equal(usr.TOTPSecret, got.TOTPSecret)
	s.Equal(usr.PasswordHash, got.PasswordHash)
	s.Equal(usr.KDFSalt, got.KDFSalt)
	s.Equal(usr.IsAdmin, got.IsAdmin)
	s.Nil(got.LastLoginAt)
}

func (s *UserRepositoryTestSuite) TestCreate_DuplicateUsername_ReturnsErrUserExists() {
	ctx := s.T().Context()

	s.Require().NoError(s.repo.Create(ctx, s.newUser("bob")))

	err := s.repo.Create(ctx, s.newUser("bob"))
	s.ErrorIs(err, domain.ErrUserExists)
}

func (s *UserRepositoryTestSuite) TestCreate_IsAdmin_StoredCorrectly() {
	ctx := s.T().Context()
	usr := s.newUser("admin")
	usr.IsAdmin = true

	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByUsername(ctx, "admin")
	s.Require().NoError(err)
	s.True(got.IsAdmin)
}

// ── GetByUsername ──

func (s *UserRepositoryTestSuite) TestGetByUsername_NotFound_ReturnsErrNotFound() {
	_, err := s.repo.GetByUsername(s.T().Context(), "nobody")
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── GetByID ──

func (s *UserRepositoryTestSuite) TestGetByID_Found() {
	ctx := s.T().Context()
	usr := s.newUser("carol")
	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal(usr.Username, got.Username)
}

func (s *UserRepositoryTestSuite) TestGetByID_NotFound_ReturnsErrNotFound() {
	_, err := s.repo.GetByID(s.T().Context(), uuid.New())
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── List ──

func (s *UserRepositoryTestSuite) TestList_Empty_ReturnsEmptySlice() {
	users, err := s.repo.List(s.T().Context())
	s.Require().NoError(err)
	s.Empty(users)
}

func (s *UserRepositoryTestSuite) TestList_OrderedByCreatedAt() {
	ctx := s.T().Context()

	first := s.newUser("dave")
	first.CreatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)

	second := s.newUser("eve")
	second.CreatedAt = time.Now().Truncate(time.Second)

	s.Require().NoError(s.repo.Create(ctx, second))
	s.Require().NoError(s.repo.Create(ctx, first))

	users, err := s.repo.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(users, 2)
	s.Equal("dave", users[0].Username)
	s.Equal("eve", users[1].Username)
}

// ── UpdateLastLogin ──

func (s *UserRepositoryTestSuite) TestUpdateLastLogin_SetsField() {
	ctx := s.T().Context()
	usr := s.newUser("frank")
	s.Require().NoError(s.repo.Create(ctx, usr))

	loginAt := time.Now().Truncate(time.Second)
	s.Require().NoError(s.repo.UpdateLastLogin(ctx, usr.ID, loginAt))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastLoginAt)
	s.Equal(loginAt.Unix(), got.LastLoginAt.Unix())
}

func (s *UserRepositoryTestSuite) TestUpdateLastLogin_NonExistentUser_NoError() {
	err := s.repo.UpdateLastLogin(s.T().Context(), uuid.New(), time.Now())
	s.Require().NoError(err)
}

// ── Revoke ──

func (s *UserRepositoryTestSuite) TestRevoke_RemovesUser() {
	ctx := s.T().Context()
	usr := s.newUser("grace")
	s.Require().NoError(s.repo.Create(ctx, usr))

	s.Require().NoError(s.repo.Revoke(ctx, usr.ID))

	_, err := s.repo.GetByID(ctx, usr.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *UserRepositoryTestSuite) TestRevoke_NonExistentUser_NoError() {
	err := s.repo.Revoke(s.T().Context(), uuid.New())
	s.Require().NoError(err)
}

// ── UpdatePassword ──

func (s *UserRepositoryTestSuite) TestUpdatePassword_UpdatesAllFields() {
	ctx := s.T().Context()
	usr := s.newUser("heidi")
	s.Require().NoError(s.repo.Create(ctx, usr))

	newHash := "new-password-hash"
	newSalt := []byte("newsaltnewsalt16")
	newTOTP := "new-totp-secret"

	s.Require().NoError(s.repo.UpdatePassword(ctx, usr.ID, newHash, newSalt, newTOTP))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(newHash, got.PasswordHash)
	s.Equal(newSalt, got.KDFSalt)
	s.Equal(newTOTP, got.TOTPSecret)
	// Other fields must not change.
	s.Equal(usr.Username, got.Username)
	s.Equal(usr.IsAdmin, got.IsAdmin)
}

func (s *UserRepositoryTestSuite) TestUpdatePassword_KDFSalt_Base64RoundTrip() {
	ctx := s.T().Context()
	usr := s.newUser("ivan")
	s.Require().NoError(s.repo.Create(ctx, usr))

	// Use bytes with all values 0–255 to exercise base64 encoding.
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i)
	}

	s.Require().NoError(s.repo.UpdatePassword(ctx, usr.ID, "h", salt, "t"))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(salt, got.KDFSalt)
}

func (s *UserRepositoryTestSuite) TestUpdatePassword_NonExistentUser_NoError() {
	err := s.repo.UpdatePassword(s.T().Context(), uuid.New(), "h", []byte("s"), "t")
	s.Require().NoError(err)
}
