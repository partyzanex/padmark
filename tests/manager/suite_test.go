package integration

import (
	"log/slog"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

var discardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

// ManagerSuite is the storage-agnostic business-logic integration suite.
// Embed it in a storage-specific suite that sets Manager in SetupTest.
type ManagerSuite struct {
	suite.Suite

	Manager *notes.Manager
}

func newManager(storage notes.Storage) *notes.Manager {
	return notes.NewManager(storage, render.NewRenderer(), crypto.New(), crypto.NewEditCodeHasher(), discardLog)
}

// ── Create ──

func (s *ManagerSuite) TestCreate_GeneratesSlugAndEditCode() {
	note := &domain.Note{Title: "hello", Content: "world"}

	result, err := s.Manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotEmpty(result.ID)
	s.Len(result.EditCode, 12)
	s.False(result.CreatedAt.IsZero())
	s.False(result.UpdatedAt.IsZero())
	s.Equal(new(domain.ContentTypeMarkdown), result.ContentType)
}

func (s *ManagerSuite) TestCreate_CustomSlug() {
	note := &domain.Note{ID: "my-slug", Title: "t", Content: "c"}

	result, err := s.Manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("my-slug", result.ID)
}

func (s *ManagerSuite) TestCreate_InvalidSlug() {
	note := &domain.Note{ID: "bad slug!", Title: "t"}

	_, err := s.Manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrInvalidSlug)
}

func (s *ManagerSuite) TestCreate_SlugConflict() {
	ctx := s.T().Context()
	note := &domain.Note{ID: "dup", Title: "t", Content: "c"}

	_, err := s.Manager.Create(ctx, note)
	s.Require().NoError(err)

	_, err = s.Manager.Create(ctx, &domain.Note{ID: "dup", Title: "t2", Content: "c2"})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerSuite) TestCreate_EmptyTitleAllowed() {
	result, err := s.Manager.Create(s.T().Context(), &domain.Note{Content: "c"})

	s.Require().NoError(err)
	s.NotEmpty(result.ID)
	s.Empty(result.Title)
}

// ── Get / expiry / burn ──

func (s *ManagerSuite) TestGet_OK() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c"})
	s.Require().NoError(err)

	got, err := s.Manager.Get(ctx, created.ID)

	s.Require().NoError(err)
	s.Equal("t", got.Title)
}

func (s *ManagerSuite) TestGet_NotFound() {
	_, err := s.Manager.Get(s.T().Context(), "nonexistent")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestGet_Expired_DeletesNote() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Minute)
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c", ExpiresAt: &past})
	s.Require().NoError(err)

	_, err = s.Manager.Get(ctx, created.ID)
	s.Require().ErrorIs(err, domain.ErrExpired)

	// note must be removed from storage
	_, err = s.Manager.Peek(ctx, created.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestGet_BurnAfterReading_ConsumedOnFirstRead() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "burn", Content: "c", BurnAfterReading: true,
	})
	s.Require().NoError(err)

	got, err := s.Manager.Get(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(created.ID, got.ID)

	_, err = s.Manager.Get(ctx, created.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestGet_BurnAfterReading_WithTTL_StartsTimer() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "burn-ttl", Content: "c", BurnAfterReading: true, BurnTTL: 3600,
	})
	s.Require().NoError(err)

	// First read: timer starts, note is still readable.
	got, err := s.Manager.Get(ctx, created.ID)
	s.Require().NoError(err)
	s.False(got.BurnAfterReading, "burn_after_reading must be cleared after timer starts")
	s.Require().NotNil(got.ExpiresAt)
	s.True(got.ExpiresAt.After(time.Now()))

	// Second read: still accessible within the TTL window.
	_, err = s.Manager.Get(ctx, created.ID)
	s.Require().NoError(err)
}

// ── Peek ──

func (s *ManagerSuite) TestPeek_ReturnsExpiredNoteWithoutDeleting() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Minute)
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c", ExpiresAt: &past})
	s.Require().NoError(err)

	// Peek must return the expired note without removing it.
	got, err := s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(created.ID, got.ID)

	// Note still exists after Peek.
	_, err = s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
}

// ── View ──

func (s *ManagerSuite) TestView_IncrementsViews() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c"})
	s.Require().NoError(err)

	_, err = s.Manager.View(ctx, created.ID)
	s.Require().NoError(err)

	_, err = s.Manager.View(ctx, created.ID)
	s.Require().NoError(err)

	got, err := s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(2, got.Views)
}

func (s *ManagerSuite) TestView_BurnAfterReading_NoViewsIncrement() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "burn", Content: "c", BurnAfterReading: true,
	})
	s.Require().NoError(err)

	got, err := s.Manager.View(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(0, got.Views)
}

// ── Update ──

func (s *ManagerSuite) TestUpdate_PreservesMetadata() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "old", Content: "c"})
	s.Require().NoError(err)

	updated, err := s.Manager.Update(ctx, created.ID, created.EditCode, &domain.Note{
		Title: "new", Content: "new content",
	})

	s.Require().NoError(err)
	s.Equal("new", updated.Title)
	s.Equal(created.EditCode, updated.EditCode)
	s.Equal(created.CreatedAt.Unix(), updated.CreatedAt.Unix())
}

func (s *ManagerSuite) TestUpdate_Forbidden() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c"})
	s.Require().NoError(err)

	_, err = s.Manager.Update(ctx, created.ID, "wrong-code", &domain.Note{Title: "hijack", Content: "x"})

	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestUpdate_NotFound() {
	_, err := s.Manager.Update(s.T().Context(), "nonexistent", "code",
		&domain.Note{Title: "t", Content: "c"})

	s.ErrorIs(err, domain.ErrNotFound)
}

// ── Delete ──

func (s *ManagerSuite) TestDelete_OK() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c"})
	s.Require().NoError(err)

	err = s.Manager.Delete(ctx, created.ID, created.EditCode)
	s.Require().NoError(err)

	_, err = s.Manager.Peek(ctx, created.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDelete_Forbidden() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "t", Content: "c"})
	s.Require().NoError(err)

	err = s.Manager.Delete(ctx, created.ID, "wrong-code")

	s.ErrorIs(err, domain.ErrForbidden)
}

// ── GetRendered ──

func (s *ManagerSuite) TestGetRendered_Markdown() {
	ctx := s.T().Context()
	created, err := s.Manager.Create(ctx, &domain.Note{Title: "md", Content: "# Hello"})
	s.Require().NoError(err)

	_, rendered, err := s.Manager.GetRendered(ctx, created.ID)

	s.Require().NoError(err)
	s.Contains(rendered, "Hello")
	s.Contains(rendered, "<h1")
}

// TestUpdate_ClearsBurnExpiryWhenBurnDisabled reproduces the bug where unchecking
// burn_after_reading in the editor left expires_at set from the burn timer.
//
// Flow: create with burn_ttl → first Get starts the timer (sets expires_at) →
// Update with burn_after_reading=false, burn_ttl=0 → expires_at must be cleared.
func (s *ManagerSuite) TestUpdate_ClearsBurnExpiryWhenBurnDisabled() {
	ctx := s.T().Context()

	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "burn-edit", Content: "c", BurnAfterReading: true, BurnTTL: 3600,
	})
	s.Require().NoError(err)

	// First read starts the burn timer — expires_at is now set.
	got, err := s.Manager.Get(ctx, created.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.ExpiresAt, "precondition: burn timer must have set expires_at")

	// User unchecks burn_after_reading in the editor.
	_, err = s.Manager.Update(ctx, created.ID, created.EditCode, &domain.Note{
		Title:            "burn-edit",
		Content:          "c",
		BurnAfterReading: false,
		BurnTTL:          0,
		// ExpiresAt intentionally nil — user cleared the burn setting.
	})
	s.Require().NoError(err)

	peek, err := s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.Nil(peek.ExpiresAt, "expires_at must be cleared when burn is disabled on update")
}

func (s *ManagerSuite) TestGetRendered_PlainText_HTMLEscaped() {
	ctx := s.T().Context()
	ct := domain.ContentTypePlain
	created, err := s.Manager.Create(ctx, &domain.Note{
		Title: "plain", Content: "<b>bold</b>", ContentType: &ct,
	})
	s.Require().NoError(err)

	_, rendered, err := s.Manager.GetRendered(ctx, created.ID)

	s.Require().NoError(err)
	s.Contains(rendered, "<pre>")
	s.Contains(rendered, "&lt;b&gt;")
	s.NotContains(rendered, "<b>")
}
