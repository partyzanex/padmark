package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/partyzanex/padmark/internal/domain"
)

type RepositoryTestSuite struct {
	suite.Suite

	db   *bun.DB
	repo *Repository
}

func (s *RepositoryTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared")
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	s.Require().NoError(s.db.Ping())

	_, err = s.db.NewCreateTable().Model((*note)(nil)).IfNotExists().Exec(s.T().Context())
	s.Require().NoError(err)

	s.repo = NewRepository(s.db)
}

func (s *RepositoryTestSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

func TestRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(RepositoryTestSuite))
}

// Create

func (s *RepositoryTestSuite) TestCreate_OK() {
	domNote := &domain.Note{
		ID:          "test-id-1",
		Title:       "hello",
		Content:     "world",
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err := s.repo.Create(s.T().Context(), domNote)

	s.Require().NoError(err)
}

func (s *RepositoryTestSuite) TestCreate_SetsID() {
	note1 := &domain.Note{ID: "id-1", Title: "a", Content: "b", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	note2 := &domain.Note{ID: "id-2", Title: "c", Content: "d", CreatedAt: time.Now(), UpdatedAt: time.Now()}

	s.Require().NoError(s.repo.Create(s.T().Context(), note1))
	s.Require().NoError(s.repo.Create(s.T().Context(), note2))

	result1, err := s.repo.Get(s.T().Context(), "id-1")
	s.Require().NoError(err)
	s.Equal("id-1", result1.ID)

	result2, err := s.repo.Get(s.T().Context(), "id-2")
	s.Require().NoError(err)
	s.Equal("id-2", result2.ID)
}

// Get

func (s *RepositoryTestSuite) TestGet_OK() {
	domNote := &domain.Note{
		ID:          "get-test-id",
		Title:       "test",
		Content:     "body",
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), domNote))

	result, err := s.repo.Get(s.T().Context(), domNote.ID)

	s.Require().NoError(err)
	s.Equal(domNote.Title, result.Title)
	s.Equal(domNote.Content, result.Content)
	s.Equal(domNote.ContentType, result.ContentType)
}

func (s *RepositoryTestSuite) TestGet_NotFound() {
	_, err := s.repo.Get(s.T().Context(), "nonexistent")

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Update

func (s *RepositoryTestSuite) TestUpdate_OK() {
	domNote := &domain.Note{
		ID:          "update-test-id",
		Title:       "old",
		Content:     "old",
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), domNote))

	updated := &domain.Note{
		Title:       "new",
		Content:     "new",
		ContentType: domain.ContentTypePlain,
		CreatedAt:   domNote.CreatedAt,
		UpdatedAt:   time.Now(),
	}
	err := s.repo.Update(s.T().Context(), domNote.ID, updated)

	s.Require().NoError(err)

	result, err := s.repo.Get(s.T().Context(), domNote.ID)

	s.Require().NoError(err)
	s.Equal("new", result.Title)
	s.Equal("new", result.Content)
	s.Equal(domain.ContentTypePlain, result.ContentType)
}

func (s *RepositoryTestSuite) TestUpdate_NotFound() {
	domNote := &domain.Note{Title: "test", Content: "test", CreatedAt: time.Now(), UpdatedAt: time.Now()}

	err := s.repo.Update(s.T().Context(), "nonexistent", domNote)

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Delete

func (s *RepositoryTestSuite) TestDelete_OK() {
	domNote := &domain.Note{
		ID:        "delete-test-id",
		Title:     "delete me",
		Content:   "x",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), domNote))

	err := s.repo.Delete(s.T().Context(), domNote.ID)

	s.Require().NoError(err)

	_, err = s.repo.Get(s.T().Context(), domNote.ID)

	s.True(errors.Is(err, domain.ErrNotFound))
}

func (s *RepositoryTestSuite) TestDelete_NotFound() {
	err := s.repo.Delete(s.T().Context(), "nonexistent")

	s.True(errors.Is(err, domain.ErrNotFound))
}
