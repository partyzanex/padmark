package notes

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type ManagerTestSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	storage  *MockStorage
	renderer *MockRenderer
	manager  *Manager
}

func (s *ManagerTestSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.renderer = NewMockRenderer(s.ctrl)
	s.manager = NewManager(s.storage, s.renderer, slog.New(slog.DiscardHandler))
}

func (s *ManagerTestSuite) TearDownTest() {
	s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

// Create

func (s *ManagerTestSuite) TestCreate_OK() {
	note := &domain.Note{Title: "hello", Content: "world"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.NotEmpty(result.ID)
	s.False(result.CreatedAt.IsZero())
	s.False(result.UpdatedAt.IsZero())
	s.Equal(domain.ContentTypeMarkdown, result.ContentType)
}

func (s *ManagerTestSuite) TestCreate_EmptyTitle() {
	_, err := s.manager.Create(s.T().Context(), &domain.Note{Content: "body"})

	s.True(errors.Is(err, domain.ErrTitleRequired))
}

func (s *ManagerTestSuite) TestCreate_ContentTooLong() {
	note := &domain.Note{Title: "hi", Content: strings.Repeat("x", maxContentLength+1)}

	_, err := s.manager.Create(s.T().Context(), note)

	s.True(errors.Is(err, domain.ErrContentTooLong))
}

func (s *ManagerTestSuite) TestCreate_InvalidContentType() {
	note := &domain.Note{Title: "hi", ContentType: "application/pdf"}

	_, err := s.manager.Create(s.T().Context(), note)

	s.True(errors.Is(err, domain.ErrInvalidContentType))
}

func (s *ManagerTestSuite) TestCreate_WithTTL() {
	future := time.Now().Add(time.Hour)
	note := &domain.Note{Title: "hello", Content: "world", ExpiresAt: &future}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotNil(result.ExpiresAt)
	s.True(result.ExpiresAt.After(time.Now()))
}

func (s *ManagerTestSuite) TestCreate_StorageError() {
	storageErr := errors.New("db error")
	note := &domain.Note{Title: "hi"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(storageErr)

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().Error(err)
	s.True(errors.Is(err, storageErr))
}

// Get

func (s *ManagerTestSuite) TestGet_OK() {
	want := &domain.Note{ID: "abc-123", Title: "a"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(want, nil)

	note, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading() {
	note := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "abc-123", Title: "a", ExpiresAt: &past}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)

	_, err := s.manager.Get(s.T().Context(), "abc-123")

	s.True(errors.Is(err, domain.ErrExpired))
}

func (s *ManagerTestSuite) TestGet_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	_, err := s.manager.Get(s.T().Context(), "missing")

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Update

func (s *ManagerTestSuite) TestUpdate_OK() {
	existing := &domain.Note{
		ID:          "abc-123",
		Title:       "old",
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	note := &domain.Note{Title: "updated", Content: "body"}

	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), "abc-123", note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", note)

	s.Require().NoError(err)
	s.Equal("abc-123", result.ID)
	s.False(result.UpdatedAt.IsZero())
	s.Equal(existing.CreatedAt, result.CreatedAt)
	s.Equal(existing.ContentType, result.ContentType)
}

func (s *ManagerTestSuite) TestUpdate_EmptyTitle() {
	_, err := s.manager.Update(s.T().Context(), "abc-123", &domain.Note{})

	s.True(errors.Is(err, domain.ErrTitleRequired))
}

func (s *ManagerTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{Title: "updated"}

	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), "missing", note)

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Delete

func (s *ManagerTestSuite) TestDelete_OK() {
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)

	err := s.manager.Delete(s.T().Context(), "abc-123")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().Delete(gomock.Any(), "missing").Return(domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing")

	s.True(errors.Is(err, domain.ErrNotFound))
}

// GetRendered

func (s *ManagerTestSuite) TestGetRendered_OK() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", html)
}

func (s *ManagerTestSuite) TestGetRendered_BurnAfterReading() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, rendered, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", rendered)
}

func (s *ManagerTestSuite) TestGetRendered_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "abc-123", Content: "# Hello", ExpiresAt: &past}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.True(errors.Is(err, domain.ErrExpired))
}

func (s *ManagerTestSuite) TestGetRendered_StorageError() {
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(nil, domain.ErrNotFound)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.True(errors.Is(err, domain.ErrNotFound))
}

func (s *ManagerTestSuite) TestGetRendered_RenderError() {
	renderErr := errors.New("render failed")
	note := &domain.Note{ID: "abc-123", Content: "bad"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.renderer.EXPECT().Render("bad").Return("", renderErr)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.True(errors.Is(err, renderErr))
}
