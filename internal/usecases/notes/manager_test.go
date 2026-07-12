package notes

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// passthroughEncryptor is a test double that returns content unchanged.
type passthroughEncryptor struct{}

func (passthroughEncryptor) Encrypt(pt, _ string) (string, error) { return pt, nil }
func (passthroughEncryptor) Decrypt(ct, _ string) (string, error) { return ct, nil }

// identityHasher is a test double that stores and compares edit codes as plaintext.
type identityHasher struct{}

func (identityHasher) Hash(code string) (string, error) { return code, nil }
func (identityHasher) Verify(hash, code string) bool    { return hash == code }

// failingEncryptor is a test double whose Decrypt always returns an error.
type failingEncryptor struct{}

func (failingEncryptor) Encrypt(pt, _ string) (string, error) { return pt, nil }
func (failingEncryptor) Decrypt(_, _ string) (string, error) {
	return "", errors.New("decryption failed")
}

// prefixEncryptor transforms content ("enc:"+pt) so the restore-to-plaintext logic is observable.
type prefixEncryptor struct{}

func (prefixEncryptor) Encrypt(pt, _ string) (string, error) { return "enc:" + pt, nil }
func (prefixEncryptor) Decrypt(ct, _ string) (string, error) {
	return strings.TrimPrefix(ct, "enc:"), nil
}

// prefixHasher transforms edit codes ("hash:"+code) so the restore-to-plaintext logic is observable.
type prefixHasher struct{}

func (prefixHasher) Hash(code string) (string, error) { return "hash:" + code, nil }
func (prefixHasher) Verify(hash, code string) bool    { return hash == "hash:"+code }

// errEncryptor fails on Encrypt (Decrypt passes through).
type errEncryptor struct{}

func (errEncryptor) Encrypt(string, string) (string, error) { return "", errors.New("encrypt boom") }
func (errEncryptor) Decrypt(ct, _ string) (string, error)   { return ct, nil }

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
	s.manager = NewManager(s.storage, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)
}

func (s *ManagerTestSuite) TearDownTest() {
	s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

// randomString

func TestRandomString_Length(t *testing.T) {
	const chars = "abc"

	for _, length := range []int{1, 5, 10, 32} {
		got := randomString(chars, length)
		assert.Len(t, got, length, "length=%d", length)
	}
}

func TestRandomString_OnlyUsesCharset(t *testing.T) {
	const chars = "xyz"

	got := randomString(chars, 100)

	for _, ch := range got {
		assert.Contains(t, chars, string(ch))
	}
}

func TestRandomString_Entropy(t *testing.T) {
	first := randomString(slugChars, slugLength)
	second := randomString(slugChars, slugLength)

	assert.NotEqual(t, first, second, "two calls should almost never produce the same string")
}

// newSlug

func TestNewSlug_LengthAndCharset(t *testing.T) {
	slug := newSlug()

	assert.Len(t, slug, slugLength, "generated slug must be slugLength chars")
	assert.Equal(t, 10, slugLength, "slug length is the configured short-URL length")

	for _, ch := range slug {
		assert.Contains(t, slugChars, string(ch), "slug must only use the slug alphabet")
	}
}

// validCustomSlug

func TestValidCustomSlug(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		// single segment (legacy shapes still valid)
		{"my-slug", true},
		{"a_b-C9", true},
		{"file.name.md", true},
		// path-like multi-segment
		{"project/GUIDE.md", true},
		{"protocol-v1/README.md", true},
		{"a/b/c/d.txt", true},
		{strings.Repeat("a", 100), true}, // at length limit
		// rejected
		{"", false},
		{"bad slug!", false},              // space / punctuation
		{"admin/x", false},                // reserved first segment
		{"edit/note", false},              // reserved first segment
		{"notes/x", false},                // reserved first segment
		{"../etc", false},                 // segment starts with dot (traversal-ish)
		{".hidden", false},                // dotfile
		{"a/.hidden", false},              // dotfile in a later segment
		{"a//b", false},                   // empty segment
		{"/leading", false},               // leading slash
		{"trailing/", false},              // trailing slash
		{strings.Repeat("a", 101), false}, // over length limit
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, validCustomSlug(tc.slug), "slug %q", tc.slug)
	}
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
	s.Equal(new(domain.ContentTypeMarkdown), result.ContentType)
}

func (s *ManagerTestSuite) TestCreate_WithCustomEditCode() {
	note := &domain.Note{Title: "hello", Content: "world", EditCode: "MyCustomCode1"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("MyCustomCode1", result.EditCode)
}

func (s *ManagerTestSuite) TestCreate_EmptyEditCodeIsGenerated() {
	note := &domain.Note{Title: "hello", Content: "world", EditCode: ""}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotEmpty(result.EditCode)
	s.Len(result.EditCode, 12)
}

func (s *ManagerTestSuite) TestCreate_CustomEditCodeUsedForUpdate() {
	customCode := "MyCustomCode1"

	note := &domain.Note{Title: "hello", Content: "world", EditCode: customCode}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	created, err := s.manager.Create(s.T().Context(), note)
	s.Require().NoError(err)
	s.Equal(customCode, created.EditCode)

	// Update with the custom code must succeed
	s.storage.EXPECT().Get(gomock.Any(), hashSlug(created.ID)).Return(created, nil)
	s.storage.EXPECT().Update(gomock.Any(), hashSlug(created.ID), gomock.Any()).Return(nil)

	updated, err := s.manager.Update(s.T().Context(), created.ID, customCode, &domain.Note{
		Title:   "updated",
		Content: "new body",
	})

	s.Require().NoError(err)
	s.Equal("updated", updated.Title)
	s.Equal(customCode, updated.EditCode)
}

func (s *ManagerTestSuite) TestCreate_CustomEditCodeWrongCodeForbidden() {
	customCode := "MyCustomCode1"

	note := &domain.Note{Title: "hello", Content: "world", EditCode: customCode}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	created, err := s.manager.Create(s.T().Context(), note)
	s.Require().NoError(err)

	// Update with wrong code must fail
	s.storage.EXPECT().Get(gomock.Any(), hashSlug(created.ID)).Return(created, nil)

	_, err = s.manager.Update(s.T().Context(), created.ID, "WrongCode1234", &domain.Note{
		Title:   "updated",
		Content: "new body",
	})

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestCreate_EmptyTitle() {
	note := &domain.Note{Content: "body"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotEmpty(result.ID)
	s.Empty(result.Title)
}

func (s *ManagerTestSuite) TestCreate_TitleTooLong() {
	note := &domain.Note{Title: strings.Repeat("x", domain.MaxTitleLength+1), Content: "body"}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrTitleTooLong)
}

func (s *ManagerTestSuite) TestCreate_InvalidContentType() {
	note := &domain.Note{Title: "hi", ContentType: new(domain.ContentType("application/pdf"))}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrInvalidContentType)
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

	s.ErrorIs(err, domain.ErrInvalidSlug)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled() {
	// With custom slugs off (allowCustomSlugs=false, the default), a create request that supplies
	// a slug is rejected — the server issues only random slugs.
	mgr := NewManager(s.storage, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), false)

	_, err := mgr.Create(s.T().Context(), &domain.Note{ID: "my-slug", Title: "hello", Content: "world"})

	s.ErrorIs(err, domain.ErrCustomSlugDisabled)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled_AutoSlugStillWorks() {
	// With custom slugs off, a create request without a slug still succeeds with a generated one.
	mgr := NewManager(s.storage, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), false)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	result, err := mgr.Create(s.T().Context(), &domain.Note{Title: "hello", Content: "world"})

	s.Require().NoError(err)
	s.Len(result.ID, slugLength)
}

func (s *ManagerTestSuite) TestCreate_StorageError() {
	storageErr := errors.New("db error")
	note := &domain.Note{Title: "hi"}
	s.storage.EXPECT().Create(gomock.Any(), note).Return(storageErr)

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().Error(err)
	s.ErrorIs(err, storageErr)
}

// TestCreate_EncryptError_RestoresPlaintext verifies that when content encryption fails,
// Create returns the error AND leaves the caller's note in plaintext (defer restore),
// not in the half-hashed state it mutates into. storage.Create must not be reached.
func (s *ManagerTestSuite) TestCreate_EncryptError_RestoresPlaintext() {
	mgr := NewManager(s.storage, s.renderer, errEncryptor{}, prefixHasher{}, slog.New(slog.DiscardHandler), true)
	note := &domain.Note{Title: "t", Content: "secret body", EditCode: "mycode123456"}

	_, err := mgr.Create(s.T().Context(), note)

	s.Require().Error(err)
	s.Contains(err.Error(), "encrypt content")
	s.Equal("secret body", note.Content, "content must be restored to plaintext")
	s.Equal("mycode123456", note.EditCode, "edit code must be restored to plaintext")
}

// TestCreate_StorageError_RestoresPlaintextAndPersistsEncrypted verifies that storage receives
// the encrypted content / hashed edit code / hashed-slug PK, and that on a storage failure the
// caller's note is restored to plaintext and the slug ID (defer restore).
func (s *ManagerTestSuite) TestCreate_StorageError_RestoresPlaintextAndPersistsEncrypted() {
	mgr := NewManager(s.storage, s.renderer, prefixEncryptor{}, prefixHasher{}, slog.New(slog.DiscardHandler), true)
	note := &domain.Note{Title: "t", Content: "body", EditCode: "code12345678", ID: "myslug"}

	var atStore domain.Note

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, n *domain.Note) error {
			atStore = *n // snapshot the row as it is persisted

			return errors.New("db error")
		})

	_, err := mgr.Create(s.T().Context(), note)
	s.Require().Error(err)

	// What hit storage: encrypted content, hashed edit code, hashed-slug primary key.
	s.Equal("enc:body", atStore.Content)
	s.Equal("hash:code12345678", atStore.EditCode)
	s.Equal(hashSlug("myslug"), atStore.ID)

	// What the caller is left with: plaintext fields and the slug ID.
	s.Equal("body", note.Content)
	s.Equal("code12345678", note.EditCode)
	s.Equal("myslug", note.ID)
}

// TestUpdate_PersistsHashedEditCode_NotPlaintext is the parity guard for the Create test: it
// asserts that the note handed to storage.Update carries the stored argon2 HASH of the edit
// code, never the plaintext the caller authenticated with. Uses prefixHasher so the hash form
// ("hash:"+code) is distinguishable from plaintext. This locks in the defense-in-depth fix so a
// future change to the repository column allow-list cannot silently start persisting plaintext.
func (s *ManagerTestSuite) TestUpdate_PersistsHashedEditCode_NotPlaintext() {
	mgr := NewManager(s.storage, s.renderer, prefixEncryptor{}, prefixHasher{}, slog.New(slog.DiscardHandler), true)

	existing := &domain.Note{ID: "myslug", EditCode: "hash:code12345678", Content: "enc:old"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("myslug")).Return(existing, nil)

	var atStore domain.Note

	s.storage.EXPECT().Update(gomock.Any(), hashSlug("myslug"), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, n *domain.Note) error {
			atStore = *n // snapshot the row as it is persisted

			return nil
		})

	updated, err := mgr.Update(s.T().Context(), "myslug", "code12345678", &domain.Note{
		Title:   "t",
		Content: "new body",
	})
	s.Require().NoError(err)

	// What hits storage: the hashed edit code (NOT the plaintext) and encrypted content.
	s.Equal("hash:code12345678", atStore.EditCode)
	s.NotEqual("code12345678", atStore.EditCode)
	s.Equal("enc:new body", atStore.Content)

	// What the caller gets back: plaintext edit code and plaintext content (defer restore).
	s.Equal("code12345678", updated.EditCode)
	s.Equal("new body", updated.Content)
}

// TestHandleSetBurnExpiryErr_RefetchDecryptFails covers the branch where, after a burn-timer
// race (SetBurnExpiry → ErrNotFound), the re-fetch succeeds but decryption of the re-fetched
// note fails — the error must propagate.
func (s *ManagerTestSuite) TestHandleSetBurnExpiryErr_RefetchDecryptFails() {
	mgr := NewManager(s.storage, s.renderer, failingEncryptor{}, identityHasher{}, slog.New(slog.DiscardHandler), true)
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-x")).Return(&domain.Note{ID: "x", Content: "ct"}, nil)

	_, err := mgr.handleSetBurnExpiryErr(s.T().Context(), "burn-x", domain.ErrNotFound)

	s.Require().Error(err)
}

// Get

func (s *ManagerTestSuite) TestGet_OK() {
	want := &domain.Note{ID: "abc-123", Title: "a"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(want, nil)

	note, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading() {
	note := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("abc-123")).Return(note, nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading_WithTTL() {
	note := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 3600}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), hashSlug("abc-123"), gomock.Any()).Return(note, nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "abc-123", Title: "a", ExpiresAt: &past}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), hashSlug("abc-123")).Return(nil)

	_, err := s.manager.Get(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestGet_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Get(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// View

func (s *ManagerTestSuite) TestView_OK() {
	want := &domain.Note{ID: "abc-123", Title: "a", Views: 5}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(want, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("abc-123")).Return(nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(6, note.Views)
}

func (s *ManagerTestSuite) TestView_BurnAfterReading() {
	want := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(want, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("abc-123")).Return(want, nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(0, note.Views) // no increment for burn-after-reading
}

func (s *ManagerTestSuite) TestView_BurnAfterReading_WithTTL() {
	future := time.Now().Add(30 * time.Minute)
	stored := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 1800}
	// Real storage flips burn_after_reading=false and sets expires_at on the timer-start read.
	afterBurn := &domain.Note{ID: "abc-123", Title: "a", BurnAfterReading: false, BurnTTL: 1800, ExpiresAt: &future}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(stored, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), hashSlug("abc-123"), gomock.Any()).Return(afterBurn, nil)
	// The note stays readable during the grace period, so each read counts.
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("abc-123")).Return(nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.False(note.BurnAfterReading)
	s.Equal(1, note.Views)
}

func (s *ManagerTestSuite) TestView_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.View(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// Update

func (s *ManagerTestSuite) TestUpdate_OK() {
	existing := &domain.Note{
		ID:          "abc-123",
		Title:       "old",
		ContentType: new(domain.ContentTypeMarkdown),
		EditCode:    "secret123456",
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	note := &domain.Note{Title: "updated", Content: "body"}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), hashSlug("abc-123"), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", note)

	s.Require().NoError(err)
	s.Equal("abc-123", result.ID)
	s.False(result.UpdatedAt.IsZero())
	s.Equal(existing.CreatedAt, result.CreatedAt)
	s.Equal(existing.ContentType, result.ContentType)
	s.Equal("secret123456", result.EditCode)
}

func (s *ManagerTestSuite) TestUpdate_EmptyTitle() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "original",
		Content:  "original body",
		EditCode: "code",
	}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "code", &domain.Note{})

	s.Require().NoError(err)
	s.NotNil(result)
}

func (s *ManagerTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{Title: "updated"}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), "missing", "code", note)

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestUpdate_Forbidden() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",
	}
	note := &domain.Note{Title: "updated"}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "abc-123", "wrong-code", note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// Delete

func (s *ManagerTestSuite) TestDelete_OK() {
	existing := &domain.Note{ID: "abc-123", EditCode: "secret123456"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), hashSlug("abc-123")).Return(nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "secret123456")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("missing")).Return(nil, domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing", "code")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestDelete_Forbidden() {
	existing := &domain.Note{ID: "abc-123", EditCode: "secret123456"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "wrong-code")

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// GetRendered

func (s *ManagerTestSuite) TestGetRendered_OK() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", html)
}

func (s *ManagerTestSuite) TestGetRendered_BurnAfterReading() {
	note := &domain.Note{ID: "abc-123", Content: "# Hello", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, rendered, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", rendered)
}

func (s *ManagerTestSuite) TestGetRendered_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "abc-123", Content: "# Hello", ExpiresAt: &past}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), hashSlug("abc-123")).Return(nil)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestGetRendered_StorageError() {
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(nil, domain.ErrNotFound)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestGetRendered_RenderError() {
	renderErr := errors.New("render failed")
	note := &domain.Note{ID: "abc-123", Content: "bad"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().Render("bad").Return("", renderErr)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.ErrorIs(err, renderErr)
}

// Peek

func (s *ManagerTestSuite) TestPeek_OK() {
	note := &domain.Note{ID: "peek-id", Title: "t", Content: "c"}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("peek-id")).Return(note, nil)

	result, err := s.manager.Peek(s.T().Context(), "peek-id")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestPeek_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Peek(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
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
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("ttl-note")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("ttl-note")).Return(nil)

	result, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(1, result.Views)

	// Second view — must NOT return ErrNotFound
	note2 := &domain.Note{ID: "ttl-note", Title: "t", Views: 1, ExpiresAt: &future}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("ttl-note")).Return(note2, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("ttl-note")).Return(nil)

	result2, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(2, result2.Views)
}

// TestView_BurnAfterReading_NoIncrementViews verifies that burn-after-reading notes
// are consumed (deleted) and IncrementViews is NOT called on the already-deleted note.
func (s *ManagerTestSuite) TestView_BurnAfterReading_NoIncrementViews() {
	note := &domain.Note{ID: "burn-note", Title: "t", BurnAfterReading: true}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-note")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("burn-note")).Return(note, nil)
	// IncrementViews must NOT be called

	result, err := s.manager.View(s.T().Context(), "burn-note")
	s.Require().NoError(err)
	s.Equal(note, result)
}

// TestView_BurnAfterReading_WithBurnTTL_CountsViews verifies that a burn-after-reading note
// with BurnTTL > 0 starts a timer on first view and, because it stays readable during the
// grace period, each read DOES increment the view counter.
// SetBurnExpiry returns the note with BurnAfterReading=false (as the real storage does after
// flipping the column), so the test exercises the actual production code path.
func (s *ManagerTestSuite) TestView_BurnAfterReading_WithBurnTTL_CountsViews() {
	future := time.Now().Add(time.Hour)
	// Real storage flips burn_after_reading=false and sets expires_at.
	noteAfterBurn := &domain.Note{ID: "burn-ttl", Title: "t", BurnAfterReading: false, BurnTTL: 3600, ExpiresAt: &future}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-ttl")).
		Return(&domain.Note{ID: "burn-ttl", Title: "t", BurnAfterReading: true, BurnTTL: 3600}, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), hashSlug("burn-ttl"), gomock.Any()).Return(noteAfterBurn, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("burn-ttl")).Return(nil)

	result, err := s.manager.View(s.T().Context(), "burn-ttl")
	s.Require().NoError(err)
	s.False(result.BurnAfterReading)
	s.NotNil(result.ExpiresAt)
	s.Equal(1, result.Views)
}

// TestGet_BurnTTL_Race_SetBurnExpiryNotFound reproduces the race condition where two concurrent
// requests both read the note (BurnAfterReading=true, BurnTTL>0) before either calls SetBurnExpiry.
//
// Timeline:
//
//	Request A: storage.Get() → note{BurnAfterReading:true, BurnTTL:3600}
//	Request B: storage.Get() → note{BurnAfterReading:true, BurnTTL:3600}  (same snapshot)
//	Request A: storage.SetBurnExpiry() → OK, DB: burn_after_reading=FALSE
//	Request B: storage.SetBurnExpiry() → ErrNotFound (WHERE burn_after_reading=TRUE matches 0 rows)
//
// Request B's note still exists — its burn timer was started by A. manager.Get must return
// the note successfully; propagating ErrNotFound here is incorrect.
func (s *ManagerTestSuite) TestGet_BurnTTL_Race_SetBurnExpiryNotFound() {
	note := &domain.Note{ID: "burn-ttl-race", Title: "t", BurnAfterReading: true, BurnTTL: 3600}

	// Request B's perspective: it read the note before A's SetBurnExpiry committed.
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-ttl-race")).Return(note, nil)
	// A already flipped burn_after_reading=false; B's conditional UPDATE matches 0 rows.
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), hashSlug("burn-ttl-race"), gomock.Any()).
		Return(nil, domain.ErrNotFound)

	// The fix must re-fetch the note to return its current state (BurnAfterReading=false, ExpiresAt set).
	future := time.Now().Add(time.Hour)
	noteAfterBurn := &domain.Note{
		ID:               "burn-ttl-race",
		Title:            "t",
		BurnAfterReading: false,
		BurnTTL:          3600,
		ExpiresAt:        &future,
	}
	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-ttl-race")).Return(noteAfterBurn, nil)

	// The note exists and its burn timer is running — Get must succeed, not return ErrNotFound.
	result, err := s.manager.Get(s.T().Context(), "burn-ttl-race")
	s.Require().NoError(err)
	s.NotNil(result)
	s.False(result.BurnAfterReading)
	s.NotNil(result.ExpiresAt)
}

// ViewPreloaded

func (s *ManagerTestSuite) TestViewPreloaded_OK() {
	note := &domain.Note{ID: "pre-1", Title: "a", Views: 2}

	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("pre-1")).Return(nil)

	result, err := s.manager.ViewPreloaded(s.T().Context(), "pre-1", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal(3, result.Views)
}

func (s *ManagerTestSuite) TestViewPreloaded_BurnAfterReading() {
	note := &domain.Note{ID: "pre-2", BurnAfterReading: true}
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("pre-2")).Return(note, nil)

	result, err := s.manager.ViewPreloaded(s.T().Context(), "pre-2", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal(0, result.Views)
}

func (s *ManagerTestSuite) TestViewPreloaded_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "pre-3", ExpiresAt: &past}

	s.storage.EXPECT().Delete(gomock.Any(), hashSlug("pre-3")).Return(nil)

	_, err := s.manager.ViewPreloaded(s.T().Context(), "pre-3", note)

	s.ErrorIs(err, domain.ErrExpired)
}

// GetRenderedPreloaded

func (s *ManagerTestSuite) TestGetRenderedPreloaded_OK() {
	note := &domain.Note{ID: "rp-1", Content: "# Hello"}

	s.storage.EXPECT().IncrementViews(gomock.Any(), hashSlug("rp-1")).Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, rendered, err := s.manager.GetRenderedPreloaded(s.T().Context(), "rp-1", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", rendered)
}

func (s *ManagerTestSuite) TestGetRenderedPreloaded_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "rp-2", ExpiresAt: &past, Content: "x"}

	s.storage.EXPECT().Delete(gomock.Any(), hashSlug("rp-2")).Return(nil)

	_, _, err := s.manager.GetRenderedPreloaded(s.T().Context(), "rp-2", note)

	s.ErrorIs(err, domain.ErrExpired)
}

// Peek — expiry policy is intentionally NOT applied

// TestPeek_ReturnsExpiredNote documents that Peek bypasses expiry policy by design.
// Callers (e.g. auth middleware) use Peek to inspect note metadata without side effects;
// they must not trigger deletion or policy enforcement.
func (s *ManagerTestSuite) TestPeek_ReturnsExpiredNote() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{ID: "expired-peek", Title: "t", ExpiresAt: &past}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("expired-peek")).Return(note, nil)
	// Delete must NOT be called — Peek does not enforce expiry.

	result, err := s.manager.Peek(s.T().Context(), "expired-peek")

	s.Require().NoError(err)
	s.Equal(note, result)
}

// Update — private notes

func (s *ManagerTestSuite) TestUpdate_PrivateNote_CorrectCode() {
	priv := true
	existing := &domain.Note{
		ID:       "priv-1",
		Title:    "secret",
		EditCode: "rightcode1234",
		Private:  &priv,
	}
	note := &domain.Note{Title: "updated secret"}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("priv-1")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), hashSlug("priv-1"), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "priv-1", "rightcode1234", note)

	s.Require().NoError(err)
	s.Equal("updated secret", result.Title)
}

func (s *ManagerTestSuite) TestUpdate_PrivateNote_WrongCode() {
	priv := true
	existing := &domain.Note{
		ID:       "priv-2",
		Title:    "secret",
		EditCode: "rightcode1234",
		Private:  &priv,
	}
	note := &domain.Note{Title: "hijacked"}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("priv-2")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "priv-2", "wrongcode0000", note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// Consume error path

func (s *ManagerTestSuite) TestGet_BurnAfterReading_ConsumeError() {
	consumeErr := errors.New("storage unavailable")
	note := &domain.Note{ID: "burn-err", Title: "t", BurnAfterReading: true}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-err")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("burn-err")).Return(nil, consumeErr)

	_, err := s.manager.Get(s.T().Context(), "burn-err")

	s.ErrorIs(err, consumeErr)
}

func (s *ManagerTestSuite) TestView_BurnAfterReading_ConsumeError() {
	consumeErr := errors.New("storage unavailable")
	note := &domain.Note{ID: "burn-err-view", Title: "t", BurnAfterReading: true}

	s.storage.EXPECT().Get(gomock.Any(), hashSlug("burn-err-view")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), hashSlug("burn-err-view")).Return(nil, consumeErr)

	_, err := s.manager.View(s.T().Context(), "burn-err-view")

	s.ErrorIs(err, consumeErr)
}

// Decryption failure

func TestGet_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, renderer, failingEncryptor{}, identityHasher{}, slog.New(slog.DiscardHandler), true)

	note := &domain.Note{ID: "enc-fail", Title: "t", Content: "ciphertext"}
	storage.EXPECT().Get(gomock.Any(), hashSlug("enc-fail")).Return(note, nil)

	_, err := mgr.Get(t.Context(), "enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPeek_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, renderer, failingEncryptor{}, identityHasher{}, slog.New(slog.DiscardHandler), true)

	note := &domain.Note{ID: "enc-fail-peek", Title: "t", Content: "ciphertext"}
	storage.EXPECT().Get(gomock.Any(), hashSlug("enc-fail-peek")).Return(note, nil)

	_, err := mgr.Peek(t.Context(), "enc-fail-peek")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestGet_BurnAfterReading_DecryptionFailure verifies that decryption failure on the initial
// storage.Get result propagates as ErrDecryptionFailed before Consume is ever called.
func TestGet_BurnAfterReading_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, renderer, failingEncryptor{}, identityHasher{}, slog.New(slog.DiscardHandler), true)

	// decryptNote runs before applyNotePolicy, so Consume is never reached.
	raw := &domain.Note{ID: "burn-enc-fail", Title: "t", BurnAfterReading: true, Content: "ciphertext"}
	storage.EXPECT().Get(gomock.Any(), hashSlug("burn-enc-fail")).Return(raw, nil)

	_, err := mgr.Get(t.Context(), "burn-enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestGet_BurnTTL_DecryptionFailure verifies that decryption failure on the initial
// storage.Get result propagates as ErrDecryptionFailed before SetBurnExpiry is ever called.
func TestGet_BurnTTL_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, renderer, failingEncryptor{}, identityHasher{}, slog.New(slog.DiscardHandler), true)

	// decryptNote runs before applyNotePolicy, so SetBurnExpiry is never reached.
	raw := &domain.Note{ID: "burn-ttl-enc-fail", Title: "t", BurnAfterReading: true, BurnTTL: 3600, Content: "ciphertext"}
	storage.EXPECT().Get(gomock.Any(), hashSlug("burn-ttl-enc-fail")).Return(raw, nil)

	_, err := mgr.Get(t.Context(), "burn-ttl-enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}
