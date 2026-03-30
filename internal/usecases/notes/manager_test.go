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
	s.NotEmpty(result.EditCode)
	s.Len(result.EditCode, 12)
	s.False(result.CreatedAt.IsZero())
	s.False(result.UpdatedAt.IsZero())
	s.Equal(domain.ContentTypeMarkdown, result.ContentType)
}

func (s *ManagerTestSuite) TestCreate_EmptyTitle() {
	_, err := s.manager.Create(s.T().Context(), &domain.Note{Content: "body"})

	s.True(errors.Is(err, domain.ErrTitleRequired))
}

func (s *ManagerTestSuite) TestCreate_TitleTooLong() {
	note := &domain.Note{Title: strings.Repeat("x", maxTitleLength+1), Content: "body"}

	_, err := s.manager.Create(s.T().Context(), note)

	s.True(errors.Is(err, domain.ErrTitleTooLong))
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

func (s *ManagerTestSuite) TestCreate_WithBurnTTL() {
	note := &domain.Note{Title: "hello", Content: "world", BurnAfterReading: true, BurnTTL: 3600}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.True(result.BurnAfterReading)
	s.Equal(int64(3600), result.BurnTTL)
	s.Nil(result.ExpiresAt) // expiry is set on first read, not at creation
}

func (s *ManagerTestSuite) TestCreate_WithSlug() {
	note := &domain.Note{ID: "my-slug", Title: "hello", Content: "world"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("my-slug", result.ID)
}

func (s *ManagerTestSuite) TestCreate_InvalidSlug() {
	note := &domain.Note{ID: "bad slug!", Title: "hello"}

	_, err := s.manager.Create(s.T().Context(), note)

	s.True(errors.Is(err, domain.ErrInvalidSlug))
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
	s.storage.EXPECT().Consume(gomock.Any(), "abc-123").Return(note, nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading_WithTTL() {
	note := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 3600}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), "abc-123", gomock.Any()).Return(note, nil)

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

// View

func (s *ManagerTestSuite) TestView_OK() {
	want := &domain.Note{ID: "abc-123", Title: "a", Views: 5}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(want, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), "abc-123").Return(nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(6, note.Views)
}

func (s *ManagerTestSuite) TestView_BurnAfterReading() {
	want := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(want, nil)
	s.storage.EXPECT().Consume(gomock.Any(), "abc-123").Return(want, nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(0, note.Views) // no increment for burn-after-reading
}

func (s *ManagerTestSuite) TestView_BurnAfterReading_WithTTL() {
	want := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 1800}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(want, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), "abc-123", gomock.Any()).Return(want, nil)
	// note stays readable during grace period — IncrementViews must NOT be called

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
}

func (s *ManagerTestSuite) TestView_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	_, err := s.manager.View(s.T().Context(), "missing")

	s.True(errors.Is(err, domain.ErrNotFound))
}

// Update

func (s *ManagerTestSuite) TestUpdate_OK() {
	existing := &domain.Note{
		ID:          "abc-123",
		Title:       "old",
		ContentType: domain.ContentTypeMarkdown,
		EditCode:    "secret123456",
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	note := &domain.Note{Title: "updated", Content: "body"}

	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), "abc-123", note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", note)

	s.Require().NoError(err)
	s.Equal("abc-123", result.ID)
	s.False(result.UpdatedAt.IsZero())
	s.Equal(existing.CreatedAt, result.CreatedAt)
	s.Equal(existing.ContentType, result.ContentType)
	s.Equal("secret123456", result.EditCode)
}

func (s *ManagerTestSuite) TestUpdate_EmptyTitle() {
	_, err := s.manager.Update(s.T().Context(), "abc-123", "code", &domain.Note{})

	s.True(errors.Is(err, domain.ErrTitleRequired))
}

func (s *ManagerTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{Title: "updated"}

	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), "missing", "code", note)

	s.True(errors.Is(err, domain.ErrNotFound))
}

func (s *ManagerTestSuite) TestUpdate_Forbidden() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",
	}
	note := &domain.Note{Title: "updated"}

	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "abc-123", "wrong-code", note)

	s.True(errors.Is(err, domain.ErrForbidden))
}

// Delete

func (s *ManagerTestSuite) TestDelete_OK() {
	existing := &domain.Note{ID: "abc-123", EditCode: "secret123456"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), "abc-123").Return(nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "secret123456")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing", "code")

	s.True(errors.Is(err, domain.ErrNotFound))
}

func (s *ManagerTestSuite) TestDelete_Forbidden() {
	existing := &domain.Note{ID: "abc-123", EditCode: "secret123456"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "wrong-code")

	s.True(errors.Is(err, domain.ErrForbidden))
}

// GetRendered

func (s *ManagerTestSuite) TestGetRendered_OK() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello"}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), "abc-123").Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", html)
}

func (s *ManagerTestSuite) TestGetRendered_BurnAfterReading() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), "abc-123").Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), "abc-123").Return(note, nil)
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
	s.storage.EXPECT().IncrementViews(gomock.Any(), "abc-123").Return(nil)
	s.renderer.EXPECT().Render("bad").Return("", renderErr)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.True(errors.Is(err, renderErr))
}

// Peek

func (s *ManagerTestSuite) TestPeek_OK() {
	note := &domain.Note{ID: "peek-id", Title: "t", Content: "c"}
	s.storage.EXPECT().Get(gomock.Any(), "peek-id").Return(note, nil)

	result, err := s.manager.Peek(s.T().Context(), "peek-id")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestPeek_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	_, err := s.manager.Peek(s.T().Context(), "missing")

	s.True(errors.Is(err, domain.ErrNotFound))
}

// View TTL regression tests

// TestView_WithTTL_SecondViewSucceeds reproduces the bug where a note with ExpiresAt set
// (but BurnAfterReading=false) was returning 404 on the second view because View was
// incorrectly calling IncrementViews for burn-after-reading notes that also had ExpiresAt set.
// For a plain TTL note (BurnAfterReading=false), multiple views must succeed.
func (s *ManagerTestSuite) TestView_WithTTL_SecondViewSucceeds() {
	future := time.Now().Add(time.Hour)
	note := &domain.Note{ID: "ttl-note", Title: "t", Views: 0, ExpiresAt: &future}

	// First view
	s.storage.EXPECT().Get(gomock.Any(), "ttl-note").Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), "ttl-note").Return(nil)

	result, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(1, result.Views)

	// Second view — must NOT return ErrNotFound
	note2 := &domain.Note{ID: "ttl-note", Title: "t", Views: 1, ExpiresAt: &future}
	s.storage.EXPECT().Get(gomock.Any(), "ttl-note").Return(note2, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), "ttl-note").Return(nil)

	result2, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(2, result2.Views)
}

// TestView_BurnAfterReading_NoIncrementViews verifies that burn-after-reading notes
// are consumed (deleted) and IncrementViews is NOT called on the already-deleted note.
func (s *ManagerTestSuite) TestView_BurnAfterReading_NoIncrementViews() {
	note := &domain.Note{ID: "burn-note", Title: "t", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), "burn-note").Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), "burn-note").Return(note, nil)
	// IncrementViews must NOT be called

	result, err := s.manager.View(s.T().Context(), "burn-note")
	s.Require().NoError(err)
	s.Equal(note, result)
}

// TestView_BurnAfterReading_WithBurnTTL_NoIncrementViews verifies that a burn-after-reading note
// with BurnTTL > 0 starts a timer on first view and IncrementViews is NOT called.
// The note remains readable during the grace period.
func (s *ManagerTestSuite) TestView_BurnAfterReading_WithBurnTTL_NoIncrementViews() {
	note := &domain.Note{ID: "burn-ttl", Title: "t", BurnAfterReading: true, BurnTTL: 3600}
	s.storage.EXPECT().Get(gomock.Any(), "burn-ttl").Return(note, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), "burn-ttl", gomock.Any()).Return(note, nil)
	// IncrementViews must NOT be called — note is in burn mode

	result, err := s.manager.View(s.T().Context(), "burn-ttl")
	s.Require().NoError(err)
	s.Equal(note, result)
}
