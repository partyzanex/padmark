//go:build integration

// Package storage runs one contract test suite per repository role on the SQLite and the
// PostgreSQL repositories. Each case runs on both dialects, so a difference between the two
// implementations fails a test (see the S4 and T1 findings of the 6fce078 architecture review).
package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// raceAttempts is how many goroutines race one single-use operation.
const raceAttempts = 16

// dialectSQLite and dialectPostgres name the backend of a suite run.
const (
	dialectSQLite   = "sqlite"
	dialectPostgres = "postgres"
)

// The interfaces below list the repository methods that the contract covers. Both dialects
// implement them with the same signatures.

type userRepo interface {
	Create(ctx context.Context, u *domain.User) error
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error
	ChangePassword(
		ctx context.Context, sess *domain.Session, passwordHash string, kdfSalt []byte, totpSecret string,
	) error
	UpdateTOTPSecret(ctx context.Context, id uuid.UUID, expected, totpSecret string) (bool, error)
	UpdateTOTPCounter(ctx context.Context, id uuid.UUID, counter int64) (bool, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

type sessionRepo interface {
	Create(ctx context.Context, s *domain.Session) error
	Get(ctx context.Context, sessionID string, now time.Time) (*domain.Session, error)
	Delete(ctx context.Context, sessionID string) error
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error
	DeleteByUserIDExcept(ctx context.Context, userID uuid.UUID, exceptSessionID string) error
	DeleteExpired(ctx context.Context, now time.Time) error
}

type apiTokenRepo interface {
	CreateIfUnderLimit(ctx context.Context, token *domain.APIToken, limit int) (bool, error)
	GetByHash(ctx context.Context, tokenHash string) (*domain.APIToken, error)
	List(ctx context.Context) ([]*domain.APIToken, error)
	RevokeByHash(ctx context.Context, tokenHash string) error
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error
	UpdateLastUsed(ctx context.Context, tokenHash string, t time.Time) error
}

type inviteRepo interface {
	Create(ctx context.Context, inv *domain.Invite) error
	DeleteStale(ctx context.Context, now time.Time) error
	RedeemInvite(ctx context.Context, tokenHash, username string, usr *domain.User, now time.Time) error
}

type revealRepo interface {
	Create(ctx context.Context, tokenHash, noteID string, expiresAt time.Time) error
	Consume(ctx context.Context, tokenHash, noteID string, now time.Time) (bool, error)
	DeleteStale(ctx context.Context, now time.Time) error
}

// noteRepo lists the note methods of the contract. Each method that compares times gets now.
type noteRepo interface {
	Create(ctx context.Context, note *domain.Note) error
	Get(ctx context.Context, id string) (*domain.Note, error)
	Consume(
		ctx context.Context, id string, expected domain.Privacy, storedEditCode string, now time.Time,
	) (*domain.Note, error)
	Update(ctx context.Context, id string, note *domain.Note, now time.Time) error
	Delete(ctx context.Context, id, storedEditCode string) error
	DeleteExpired(ctx context.Context, id string, now time.Time) error
}

// linkRepo lists the link methods of the contract. ConsumeVisible, IncrementViews and
// IncrementViewsVisible are not in the contract yet.
type linkRepo interface {
	Create(ctx context.Context, link *domain.Link) error
	GetByID(ctx context.Context, id string) (*domain.Link, error)
	Consume(ctx context.Context, id string, now time.Time) (*domain.Link, error)
	Update(
		ctx context.Context, id, storedEditCode string, patch *domain.LinkPatch, now time.Time,
	) (*domain.Link, error)
	Delete(ctx context.Context, id, storedEditCode string) error
	DeleteExpired(ctx context.Context, id string, now time.Time) error
}

// fileRepo lists the file methods of the contract. ClaimBurn, ReleaseBurn and
// IncrementDownloadsVisible are not in the contract yet.
type fileRepo interface {
	Create(ctx context.Context, file *domain.File) error
	GetByID(ctx context.Context, id string, now time.Time) (*domain.File, error)
	Update(ctx context.Context, id, objectKey string, opts files.UpdateOptions, now time.Time) error
	Delete(ctx context.Context, id, objectKey string) error
	ListExpired(ctx context.Context, now time.Time, after files.ExpiredCursor, limit int) ([]*domain.File, error)
	DeleteExpired(ctx context.Context, id, objectKey string, now time.Time) error
}

// backend is one empty, migrated database and the repositories of one dialect.
type backend struct {
	db       *bun.DB
	users    userRepo
	sessions sessionRepo
	tokens   apiTokenRepo
	invites  inviteRepo
	reveals  revealRepo
	notes    noteRepo
	links    linkRepo
	files    fileRepo
	dialect  string
}

// opener returns a backend with an empty database for one test.
type opener func(t *testing.T) *backend

// repoSuite is the base of each contract suite. SetupTest gives each test an empty database.
type repoSuite struct {
	suite.Suite

	open opener
	be   *backend
}

func (s *repoSuite) SetupTest() {
	s.be = s.open(s.T())
}

// contractSuite is a suite that embeds repoSuite.
type contractSuite interface {
	suite.TestingSuite
	setOpener(open opener)
}

func (s *repoSuite) setOpener(open opener) {
	s.open = open
}

// runContract runs each contract suite on the backend that open gives.
func runContract(t *testing.T, open opener) {
	t.Helper()

	suites := []struct {
		name  string
		suite contractSuite
	}{
		{name: "users", suite: new(UserSuite)},
		{name: "sessions", suite: new(SessionSuite)},
		{name: "api_tokens", suite: new(APITokenSuite)},
		{name: "invites", suite: new(InviteSuite)},
		{name: "reveal_tokens", suite: new(RevealSuite)},
		{name: "notes", suite: new(NoteSuite)},
		{name: "links", suite: new(LinkSuite)},
		{name: "files", suite: new(FileSuite)},
	}

	for _, entry := range suites {
		entry.suite.setOpener(open)
		t.Run(entry.name, func(t *testing.T) { suite.Run(t, entry.suite) })
	}
}

// --- helpers ---

// now is the clock of the contract: it always returns the same time. The repositories never
// read the clock: each method that compares times gets now as a parameter, so the expiry cases
// do not depend on the real clock. The time has whole seconds, because PostgreSQL keeps only
// microseconds.
func now() time.Time {
	return time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
}

func newUser(username string, isAdmin bool) *domain.User {
	return &domain.User{
		ID:              uuid.New(),
		Username:        username,
		TOTPSecret:      "sealed-secret",
		PasswordHash:    "password-hash",
		KDFSalt:         []byte("kdf-salt-16bytes"),
		IsAdmin:         isAdmin,
		LastTOTPCounter: 0,
		CreatedAt:       now(),
		LastLoginAt:     nil,
	}
}

func (s *repoSuite) createUser(username string, isAdmin bool) *domain.User {
	s.T().Helper()

	usr := newUser(username, isAdmin)
	s.Require().NoError(s.be.users.Create(s.T().Context(), usr))

	return usr
}

func newSession(hash string, userID uuid.UUID, expiresAt time.Time) *domain.Session {
	return &domain.Session{
		SessionHash: hash,
		UserID:      userID,
		CreatedAt:   now(),
		ExpiresAt:   expiresAt,
		UserAgent:   "contract-test",
		IP:          "127.0.0.1",
	}
}

func newAPIToken(hash string, userID uuid.UUID, expiresAt *time.Time) *domain.APIToken {
	return &domain.APIToken{
		TokenHash:  hash,
		UserID:     userID,
		CreatedAt:  now(),
		ExpiresAt:  expiresAt,
		LastUsedAt: nil,
		IssuedBy:   nil,
	}
}

func newInvite(hash string, createdBy uuid.UUID, expiresAt time.Time) *domain.Invite {
	return &domain.Invite{
		TokenHash: hash,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
		UsedAt:    nil,
		UsedBy:    "",
	}
}

func newNote(id, editCode string, expiresAt *time.Time) *domain.Note {
	return &domain.Note{
		ID:               id,
		Title:            "title",
		Content:          "ciphertext",
		ContentType:      new(domain.ContentTypeMarkdown),
		EditCode:         editCode,
		CreatedAt:        now(),
		UpdatedAt:        now(),
		ExpiresAt:        expiresAt,
		BurnTTL:          0,
		BurnAfterReading: false,
		Privacy:          new(domain.PrivacyPublic),
		OwnerID:          nil,
		ShowContents:     nil,
		Views:            0,
		ClearExpiry:      false,
	}
}

func newLink(id string, expiresAt *time.Time) *domain.Link {
	return &domain.Link{
		ID:             id,
		TargetURL:      "ciphertext-url",
		EditCode:       "edit-hash",
		CreatedAt:      now(),
		ExpiresAt:      expiresAt,
		Privacy:        new(domain.PrivacyPublic),
		OwnerID:        nil,
		DirectRedirect: nil,
		Views:          0,
		BurnAfterClick: false,
	}
}

func newFile(id, objectKey string, expiresAt *time.Time) *domain.File {
	return &domain.File{
		ID:                id,
		ObjectKey:         objectKey,
		SealedMeta:        "sealed-meta",
		EditCode:          "file-edit-hash",
		CreatedAt:         now(),
		ExpiresAt:         expiresAt,
		Privacy:           new(domain.PrivacyPublic),
		OwnerID:           nil,
		Size:              42,
		Downloads:         0,
		BurnAfterDownload: false,
		Name:              "",
		ContentKey:        nil,
	}
}

// race runs operation raceAttempts times at the same time and returns the results by attempt.
func race[T any](operation func(attempt int) T) []T {
	results := make([]T, raceAttempts)
	start := make(chan struct{})

	var group sync.WaitGroup

	for attempt := range raceAttempts {
		group.Go(func() {
			<-start

			results[attempt] = operation(attempt)
		})
	}

	close(start)
	group.Wait()

	return results
}

// countTrue returns the number of true values.
func countTrue(results []bool) int {
	won := 0

	for _, ok := range results {
		if ok {
			won++
		}
	}

	return won
}
