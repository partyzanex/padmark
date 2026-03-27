package notes

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

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
	s.False(result.CreatedAt.IsZero())
	s.False(result.UpdatedAt.IsZero())
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

func (s *ManagerTestSuite) TestCreate_StorageError() {
	storageErr := errors.New("db error")
	note := &domain.Note{Title: "hi"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(storageErr)

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().Error(err)
	s.True(errors.Is(err, storageErr))
}

// List

func (s *ManagerTestSuite) TestList_OK() {
	want := []domain.Note{{ID: 1, Title: "a"}, {ID: 2, Title: "b"}}
	s.storage.EXPECT().List(gomock.Any(), 10, 0).Return(want, 2, nil)

	notes, total, err := s.manager.List(s.T().Context(), 10, 0)

	s.Require().NoError(err)
	s.Equal(want, notes)
	s.Equal(2, total)
}

func (s *ManagerTestSuite) TestList_StorageError() {
	storageErr := errors.New("db error")
	s.storage.EXPECT().List(gomock.Any(), 10, 0).Return(nil, 0, storageErr)

	_, _, err := s.manager.List(s.T().Context(), 10, 0)

	s.True(errors.Is(err, storageErr))
}

// Get

func (s *ManagerTestSuite) TestGet_OK() {
	want := &domain.Note{ID: 1, Title: "a"}
	s.storage.EXPECT().Get(gomock.Any(), int64(1)).Return(want, nil)

	note, err := s.manager.Get(s.T().Context(), 1)

	s.Require().NoError(err)
	s.Equal(want, note)
}

func (s *ManagerTestSuite) TestGet_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), int64(99)).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Get(s.T().Context(), 99)

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Update

func (s *ManagerTestSuite) TestUpdate_OK() {
	note := &domain.Note{Title: "updated", Content: "body"}
	s.storage.EXPECT().Update(gomock.Any(), int64(1), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), 1, note)

	s.Require().NoError(err)
	s.Equal(int64(1), result.ID)
	s.False(result.UpdatedAt.IsZero())
}

func (s *ManagerTestSuite) TestUpdate_EmptyTitle() {
	_, err := s.manager.Update(s.T().Context(), 1, &domain.Note{})

	s.True(errors.Is(err, domain.ErrTitleRequired))
}

func (s *ManagerTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{Title: "updated"}
	s.storage.EXPECT().Update(gomock.Any(), int64(99), note).Return(domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), 99, note)

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Delete

func (s *ManagerTestSuite) TestDelete_OK() {
	s.storage.EXPECT().Delete(gomock.Any(), int64(1)).Return(nil)

	err := s.manager.Delete(s.T().Context(), 1)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().Delete(gomock.Any(), int64(99)).Return(domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), 99)

	s.True(errors.Is(err, domain.ErrNotFound))
}

// GetRendered

func (s *ManagerTestSuite) TestGetRendered_OK() {
	note := &domain.Note{ID: 1, Content: "# Hello"}
	s.storage.EXPECT().Get(gomock.Any(), int64(1)).Return(note, nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	html, err := s.manager.GetRendered(s.T().Context(), 1)

	s.Require().NoError(err)
	s.Equal("<h1>Hello</h1>", html)
}

func (s *ManagerTestSuite) TestGetRendered_StorageError() {
	s.storage.EXPECT().Get(gomock.Any(), int64(1)).Return(nil, domain.ErrNotFound)

	_, err := s.manager.GetRendered(s.T().Context(), 1)

	s.True(errors.Is(err, domain.ErrNotFound))
}

func (s *ManagerTestSuite) TestGetRendered_RenderError() {
	renderErr := errors.New("render failed")
	note := &domain.Note{ID: 1, Content: "bad"}
	s.storage.EXPECT().Get(gomock.Any(), int64(1)).Return(note, nil)
	s.renderer.EXPECT().Render("bad").Return("", renderErr)

	_, err := s.manager.GetRendered(s.T().Context(), 1)

	s.True(errors.Is(err, renderErr))
}
