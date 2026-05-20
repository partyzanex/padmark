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
		ContentType: new(domain.ContentTypeMarkdown),
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
		ContentType: new(domain.ContentTypeMarkdown),
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

	s.ErrorIs(err, domain.ErrNotFound)
}

// Update

func (s *RepositoryTestSuite) TestUpdate_OK() {
	domNote := &domain.Note{
		ID:          "update-test-id",
		Title:       "old",
		Content:     "old",
		ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), domNote))

	updated := &domain.Note{
		Title:       "new",
		Content:     "new",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   domNote.CreatedAt,
		UpdatedAt:   time.Now(),
	}
	err := s.repo.Update(s.T().Context(), domNote.ID, updated)

	s.Require().NoError(err)

	result, err := s.repo.Get(s.T().Context(), domNote.ID)

	s.Require().NoError(err)
	s.Equal("new", result.Title)
	s.Equal("new", result.Content)
	s.Equal(new(domain.ContentTypePlain), result.ContentType)
}

func (s *RepositoryTestSuite) TestUpdate_NotFound() {
	domNote := &domain.Note{Title: "test", Content: "test", CreatedAt: time.Now(), UpdatedAt: time.Now()}

	err := s.repo.Update(s.T().Context(), "nonexistent", domNote)

	s.ErrorIs(err, domain.ErrNotFound)
}

// TestUpdate_Private_Nil_PreservesExistingValue verifies that passing Private=nil to Update
// keeps the existing private value in the DB (via COALESCE(NULL, private)).
func (s *RepositoryTestSuite) TestUpdate_Private_Nil_PreservesExistingValue() {
	ctx := s.T().Context()

	note := &domain.Note{
		ID:          "priv-nil-1",
		Title:       "t",
		Content:     "c",
		ContentType: new(domain.ContentTypeMarkdown),
		Private:     new(true),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, note))

	// Update with Private=nil — must not touch the private column.
	updated := &domain.Note{
		Title:     "t2",
		Content:   "c2",
		Private:   nil,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.Require().NoError(s.repo.Update(ctx, "priv-nil-1", updated))

	got, err := s.repo.Get(ctx, "priv-nil-1")
	s.Require().NoError(err)
	s.True(got.Private != nil && *got.Private, "private must be preserved when Update receives Private=nil")
}

// TestUpdate_Private_ExplicitFalse_ClearsPrivacy verifies that passing Private=&false
// explicitly sets the column to false, even if the note was previously private.
func (s *RepositoryTestSuite) TestUpdate_Private_ExplicitFalse_ClearsPrivacy() {
	ctx := s.T().Context()

	note := &domain.Note{
		ID:          "priv-false-1",
		Title:       "t",
		Content:     "c",
		ContentType: new(domain.ContentTypeMarkdown),
		Private:     new(true),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, note))

	updated := &domain.Note{
		Title:     "t2",
		Content:   "c2",
		Private:   new(false),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.Require().NoError(s.repo.Update(ctx, "priv-false-1", updated))

	got, err := s.repo.Get(ctx, "priv-false-1")
	s.Require().NoError(err)
	s.False(got.Private != nil && *got.Private, "private must be cleared when Update receives Private=&false")
}

// TestUpdate_ContentType_Nil_PreservesExistingValue verifies that passing ContentType=nil to Update
// keeps the existing content_type in the DB (via COALESCE(NULL, content_type)).
func (s *RepositoryTestSuite) TestUpdate_ContentType_Nil_PreservesExistingValue() {
	ctx := s.T().Context()

	note := &domain.Note{
		ID:          "ct-nil-1",
		Title:       "t",
		Content:     "c",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, note))

	updated := &domain.Note{
		Title:       "t2",
		Content:     "c2",
		ContentType: nil,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Update(ctx, "ct-nil-1", updated))

	got, err := s.repo.Get(ctx, "ct-nil-1")
	s.Require().NoError(err)
	s.Equal(new(domain.ContentTypePlain), got.ContentType,
		"content_type must be preserved when Update receives ContentType=nil")
}

// TestUpdate_ContentType_ExplicitChange verifies that passing ContentType=&markdown changes the column.
func (s *RepositoryTestSuite) TestUpdate_ContentType_ExplicitChange() {
	ctx := s.T().Context()

	note := &domain.Note{
		ID:          "ct-change-1",
		Title:       "t",
		Content:     "c",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, note))

	updated := &domain.Note{
		Title:       "t2",
		Content:     "c2",
		ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Update(ctx, "ct-change-1", updated))

	got, err := s.repo.Get(ctx, "ct-change-1")
	s.Require().NoError(err)
	s.Equal(new(domain.ContentTypeMarkdown), got.ContentType, "content_type must be updated when explicitly set")
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

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositoryTestSuite) TestDelete_NotFound() {
	err := s.repo.Delete(s.T().Context(), "nonexistent")

	s.ErrorIs(err, domain.ErrNotFound)
}

// DuplicateSlug

func (s *RepositoryTestSuite) TestCreate_DuplicateSlug() {
	ctx := s.T().Context()

	n := &domain.Note{ID: "dup-1", Title: "a", Content: "b", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, n))

	dup := &domain.Note{ID: "dup-1", Title: "c", Content: "d", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	err := s.repo.Create(ctx, dup)

	s.ErrorIs(err, domain.ErrSlugConflict)
}

// AllFields

func (s *RepositoryTestSuite) TestCreate_AllFields() {
	ctx := s.T().Context()

	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	note := &domain.Note{
		ID:               "all-fields",
		Title:            "title",
		Content:          "content",
		ContentType:      new(domain.ContentTypePlain),
		EditCode:         "s3cr3t",
		ExpiresAt:        &future,
		BurnAfterReading: true,
		BurnTTL:          3600,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, note))

	got, err := s.repo.Get(ctx, "all-fields")
	s.Require().NoError(err)
	s.Equal("s3cr3t", got.EditCode)
	s.True(got.BurnAfterReading)
	s.Equal(int64(3600), got.BurnTTL)
	s.Equal(new(domain.ContentTypePlain), got.ContentType)
	s.NotNil(got.ExpiresAt)
}

// IncrementViews

func (s *RepositoryTestSuite) TestIncrementViews_OK() {
	ctx := s.T().Context()

	n := &domain.Note{ID: "views-ok", Title: "t", Content: "c", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, n))

	s.Require().NoError(s.repo.IncrementViews(ctx, "views-ok"))
	s.Require().NoError(s.repo.IncrementViews(ctx, "views-ok"))

	got, err := s.repo.Get(ctx, "views-ok")
	s.Require().NoError(err)
	s.Equal(2, got.Views)
}

func (s *RepositoryTestSuite) TestIncrementViews_NotFound() {
	err := s.repo.IncrementViews(s.T().Context(), "views-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// Consume

func (s *RepositoryTestSuite) TestConsume_BurnAfterReading() {
	ctx := s.T().Context()

	n := &domain.Note{
		ID: "consume-bar", Title: "burn", Content: "me",
		BurnAfterReading: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, n))

	got, err := s.repo.Consume(ctx, "consume-bar")
	s.Require().NoError(err)
	s.Equal("consume-bar", got.ID)

	_, err = s.repo.Get(ctx, "consume-bar")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositoryTestSuite) TestConsume_Expired() {
	ctx := s.T().Context()

	past := time.Now().Add(-time.Hour)
	n := &domain.Note{
		ID: "consume-exp", Title: "expired", Content: "me",
		ExpiresAt: &past, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, n))

	got, err := s.repo.Consume(ctx, "consume-exp")
	s.Require().NoError(err)
	s.Equal("consume-exp", got.ID)

	_, err = s.repo.Get(ctx, "consume-exp")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositoryTestSuite) TestConsume_NotEligible() {
	ctx := s.T().Context()

	n := &domain.Note{ID: "consume-no", Title: "plain", Content: "me", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, n))

	_, err := s.repo.Consume(ctx, "consume-no")
	s.Require().ErrorIs(err, domain.ErrNotFound)

	// note must still exist
	got, err := s.repo.Get(ctx, "consume-no")
	s.Require().NoError(err)
	s.Equal("consume-no", got.ID)
}

func (s *RepositoryTestSuite) TestConsume_NotFound() {
	_, err := s.repo.Consume(s.T().Context(), "consume-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// Migrate

func (s *RepositoryTestSuite) TestMigrate_OK() {
	ctx := s.T().Context()

	sqldb, err := sql.Open(sqliteshim.DriverName(), "file:migrate_ok?mode=memory&cache=shared")
	s.Require().NoError(err)

	db := bun.NewDB(sqldb, sqlitedialect.New())

	defer func() { s.Require().NoError(db.Close()) }()

	_, migrateErr := Migrate(ctx, db)
	s.Require().NoError(migrateErr)
}

func (s *RepositoryTestSuite) TestMigrate_Idempotent() {
	ctx := s.T().Context()

	sqldb, err := sql.Open(sqliteshim.DriverName(), "file:migrate_idem?mode=memory&cache=shared")
	s.Require().NoError(err)

	db := bun.NewDB(sqldb, sqlitedialect.New())

	defer func() { s.Require().NoError(db.Close()) }()

	_, migrateErr := Migrate(ctx, db)
	s.Require().NoError(migrateErr)

	_, migrateErr2 := Migrate(ctx, db)
	s.Require().NoError(migrateErr2)
}
