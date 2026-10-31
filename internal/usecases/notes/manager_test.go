package notes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// testUserID1 and testUserID2 are fixed fake user IDs for owner-bypass tests: distinct
// identities that must NOT compare equal, standing in for "the note's owner" and "some other
// authenticated caller" respectively.
var (
	testUserID1 = uuid.New() //nolint:gochecknoglobals // test fixture
	testUserID2 = uuid.New() //nolint:gochecknoglobals // test fixture
)

// passthroughEncryptor is a test double that returns content unchanged.
type passthroughEncryptor struct{}

func (passthroughEncryptor) Encrypt(pt, _ string) (string, error)    { return pt, nil }
func (passthroughEncryptor) Decrypt(ct, _, _ string) (string, error) { return ct, nil }

// identityHasher is a test double that stores and compares edit codes as plaintext.
type identityHasher struct{}

func (identityHasher) Hash(code string) (string, error) { return code, nil }

// HashUserProvided mirrors Hash but prefixes the code, so tests can assert which hashing
// variant the usecase picked for caller-supplied codes (see
// TestCreate_UserSuppliedCodeUsesMemoryHardHash).
func (identityHasher) HashUserProvided(code string) (string, error) { return "user:" + code, nil }

func (identityHasher) Verify(hash, code string) bool { return hash == code || hash == "user:"+code }

// failingEncryptor is a test double whose Decrypt always returns an error.
type failingEncryptor struct{}

func (failingEncryptor) Encrypt(pt, _ string) (string, error) { return pt, nil }
func (failingEncryptor) Decrypt(_, _, _ string) (string, error) {
	return "", errors.New("decryption failed")
}

// prefixEncryptor transforms content ("enc:"+pt) so the restore-to-plaintext logic is observable.
type prefixEncryptor struct{}

func (prefixEncryptor) Encrypt(pt, _ string) (string, error) { return "enc:" + pt, nil }
func (prefixEncryptor) Decrypt(ct, _, _ string) (string, error) {
	return strings.TrimPrefix(ct, "enc:"), nil
}

// prefixHasher transforms edit codes ("hash:"+code) so the restore-to-plaintext logic is observable.
type prefixHasher struct{}

func (prefixHasher) Hash(code string) (string, error) { return "hash:" + code, nil }

func (prefixHasher) HashUserProvided(code string) (string, error) { return "userhash:" + code, nil }

func (prefixHasher) Verify(hash, code string) bool {
	return hash == "hash:"+code || hash == "userhash:"+code
}

// errEncryptor fails on Encrypt (Decrypt passes through).
type errEncryptor struct{}

func (errEncryptor) Encrypt(string, string) (string, error)  { return "", errors.New("encrypt boom") }
func (errEncryptor) Decrypt(ct, _, _ string) (string, error) { return ct, nil }

// noopSlugLock is a test double that grants every lock immediately — these tests exercise
// business logic in isolation, never two concurrent Create calls, so nothing here needs the
// real cross-manager serialization internal/infra/lock.Locker provides in production.
type noopSlugLock struct{}

func (noopSlugLock) Lock(context.Context, string) (func(), error) { return func() {}, nil }

type ManagerTestSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	storage  *MockStorage
	links    *MockLinksChecker
	renderer *MockRenderer
	manager  *Manager
}

func (s *ManagerTestSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.links = NewMockLinksChecker(s.ctrl)
	s.renderer = NewMockRenderer(s.ctrl)
	s.manager = NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)
}

// expectSlugFree sets up s.links to report the candidate slug as not already taken by a link —
// the default, common case for Create tests that are expected to reach storage.Create.
func (s *ManagerTestSuite) expectSlugFree() {
	s.links.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
}

func (s *ManagerTestSuite) TearDownTest() {
	s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

// Create

func (s *ManagerTestSuite) TestCreate_OK() {
	note := &domain.Note{
		Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.expectSlugFree()
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
	note := &domain.Note{
		Title: "hello", Content: "world", EditCode: "MyCustomCode1",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("MyCustomCode1", result.EditCode)
}

// TestCreate_UserSuppliedCodeUsesMemoryHardHash pins that a caller-chosen edit code is
// hashed with the memory-hard variant (identityHasher.HashUserProvided → "user:" prefix)
// because its entropy is not guaranteed.
func (s *ManagerTestSuite) TestCreate_UserSuppliedCodeUsesMemoryHardHash() {
	note := &domain.Note{
		Title: "hello", Content: "world", EditCode: "MyCustomCode1",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	var storedHash string

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stored *domain.Note) error {
			storedHash = stored.EditCode

			return nil
		})

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("user:MyCustomCode1", storedHash)
}

// TestCreate_GeneratedCodeUsesFastHash pins the mirror invariant: a server-generated code
// stays on the fast-hash path (identityHasher.Hash → no prefix).
func (s *ManagerTestSuite) TestCreate_GeneratedCodeUsesFastHash() {
	note := &domain.Note{
		Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	var storedHash string

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stored *domain.Note) error {
			storedHash = stored.EditCode

			return nil
		})

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Len(storedHash, 12)
	s.NotContains(storedHash, "user:")
}

func (s *ManagerTestSuite) TestCreate_EmptyEditCodeIsGenerated() {
	note := &domain.Note{
		Title: "hello", Content: "world", EditCode: "", CreatedAt: time.Time{},
		UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		ID: "", ContentType: nil, Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotEmpty(result.EditCode)
	s.Len(result.EditCode, 12)
}

func (s *ManagerTestSuite) TestCreate_CustomEditCodeUsedForUpdate() {
	customCode := "MyCustomCode1"

	note := &domain.Note{
		Title: "hello", Content: "world", EditCode: customCode,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	created, err := s.manager.Create(s.T().Context(), note)
	s.Require().NoError(err)
	s.Equal(customCode, created.EditCode)

	// Update with the custom code must succeed
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug(created.ID)).Return(created, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug(created.ID), gomock.Any()).Return(nil)

	updated, err := s.manager.Update(s.T().Context(), created.ID, customCode, uuid.Nil, &domain.Note{
		Title:   "updated",
		Content: "new body",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	})

	s.Require().NoError(err)
	s.Equal("updated", updated.Title)
	s.Equal(customCode, updated.EditCode)
}

func (s *ManagerTestSuite) TestCreate_CustomEditCodeWrongCodeForbidden() {
	customCode := "MyCustomCode1"

	note := &domain.Note{
		Title: "hello", Content: "world", EditCode: customCode,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	created, err := s.manager.Create(s.T().Context(), note)
	s.Require().NoError(err)

	// Update with wrong code must fail
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug(created.ID)).Return(created, nil)

	_, err = s.manager.Update(s.T().Context(), created.ID, "WrongCode1234", uuid.Nil, &domain.Note{
		Title:   "updated",
		Content: "new body",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	})

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestCreate_EmptyTitle() {
	note := &domain.Note{
		Content:   "body",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.NotEmpty(result.ID)
	s.Empty(result.Title)
}

func (s *ManagerTestSuite) TestCreate_TitleTooLong() {
	note := &domain.Note{
		Title: strings.Repeat("x", domain.MaxTitleLength+1), Content: "body",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrTitleTooLong)
}

func (s *ManagerTestSuite) TestCreate_InvalidContentType() {
	note := &domain.Note{
		Title: "hi", ContentType: new(domain.ContentType("application/pdf")),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrInvalidContentType)
}

// TestCreate_OwnerPrivacy_NoOwnerID_Rejected verifies that a note cannot be created with
// privacy=owner and no OwnerID (e.g. an anonymous caller) — such a note could never be read
// back by anyone, including its own creator.
func (s *ManagerTestSuite) TestCreate_OwnerPrivacy_NoOwnerID_Rejected() {
	priv := domain.PrivacyOwner
	note := &domain.Note{
		Title: "hi", Content: "body", Privacy: &priv,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrOwnerPrivacyRequiresOwner)
}

// TestCreate_OwnerPrivacy_WithOwnerID_OK verifies that privacy=owner is accepted when the note
// actually has an OwnerID (the HTTP layer sets this from the authenticated caller before Create).
func (s *ManagerTestSuite) TestCreate_OwnerPrivacy_WithOwnerID_OK() {
	priv := domain.PrivacyOwner
	note := &domain.Note{
		Title: "hi", Content: "body", Privacy: &priv, OwnerID: new(testUserID1),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		ID: "", ContentType: nil, EditCode: "", Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal(domain.PrivacyOwner, result.EffectivePrivacy())
}

func (s *ManagerTestSuite) TestCreate_WithBurnTTL() {
	note := &domain.Note{
		Title: "hello", Content: "world", BurnAfterReading: true, BurnTTL: 3600,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "", Views: 0,
		ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.True(result.BurnAfterReading)
	s.Equal(int64(3600), result.BurnTTL)
	s.Nil(result.ExpiresAt) // expiry is set on first read, not at creation
}

func (s *ManagerTestSuite) TestCreate_WithSlug() {
	note := &domain.Note{
		ID: "my-slug", Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(nil)

	result, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
	s.Equal("my-slug", result.ID)
}

func (s *ManagerTestSuite) TestCreate_InvalidSlug() {
	note := &domain.Note{
		ID: "bad slug!", Title: "hello",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrInvalidSlug)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled() {
	// With custom slugs off (allowCustomSlugs=false, the default), a create request that supplies
	// a slug is rejected — the server issues only random slugs.
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), false)

	_, err := mgr.Create(s.T().Context(), &domain.Note{
		ID: "my-slug", Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	})

	s.ErrorIs(err, domain.ErrCustomSlugDisabled)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled_AutoSlugStillWorks() {
	// With custom slugs off, a create request without a slug still succeeds with a generated one.
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), false)
	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	result, err := mgr.Create(s.T().Context(), &domain.Note{
		Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	})

	s.Require().NoError(err)
	// Exact generated-slug length is domain.ResolveSlug's contract, covered by
	// domain.TestNewSlug_LengthAndCharset — this test only needs to know one was generated.
	s.NotEmpty(result.ID)
}

func (s *ManagerTestSuite) TestCreate_StorageError() {
	storageErr := errors.New("db error")
	note := &domain.Note{
		Title:     "hi",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), note).Return(storageErr)

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().Error(err)
	s.ErrorIs(err, storageErr)
}

// TestCreate_EncryptError_RestoresPlaintext verifies that when content encryption fails,
// Create returns the error AND leaves the caller's note in plaintext (defer restore),
// not in the half-hashed state it mutates into. storage.Create must not be reached.
func (s *ManagerTestSuite) TestCreate_EncryptError_RestoresPlaintext() {
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, errEncryptor{}, prefixHasher{},
		slog.New(slog.DiscardHandler), true)
	note := &domain.Note{
		Title: "t", Content: "secret body", EditCode: "mycode123456",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()

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
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, prefixEncryptor{}, prefixHasher{},
		slog.New(slog.DiscardHandler), true)
	note := &domain.Note{
		Title: "t", Content: "body", EditCode: "code12345678", ID: "myslug",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.expectSlugFree()

	var atStore domain.Note

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, n *domain.Note) error {
			atStore = *n // snapshot the row as it is persisted

			return errors.New("db error")
		})

	_, err := mgr.Create(s.T().Context(), note)
	s.Require().Error(err)

	// What hit storage: encrypted content, hashed edit code, hashed-slug primary key.
	// The code is caller-supplied, so it takes the memory-hard variant ("userhash:").
	s.Equal("enc:body", atStore.Content)
	s.Equal("userhash:code12345678", atStore.EditCode)
	s.Equal(domain.HashSlug("myslug"), atStore.ID)

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
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, prefixEncryptor{}, prefixHasher{},
		slog.New(slog.DiscardHandler), true)

	existing := &domain.Note{
		ID: "myslug", EditCode: "hash:code12345678", Content: "enc:old",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("myslug")).Return(existing, nil)

	var atStore domain.Note

	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("myslug"), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, n *domain.Note) error {
			atStore = *n // snapshot the row as it is persisted

			return nil
		})

	updated, err := mgr.Update(s.T().Context(), "myslug", "code12345678", uuid.Nil, &domain.Note{
		Title:   "t",
		Content: "new body",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
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
	mgr := NewManager(s.storage, s.links, noopSlugLock{}, s.renderer, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-x")).Return(&domain.Note{
		ID: "x", Content: "ct",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}, nil)

	_, err := mgr.burn.handleSetExpiryErr(s.T().Context(), "burn-x", domain.ErrNotFound)

	s.Require().Error(err)
}

// Get

func (s *ManagerTestSuite) TestGet_OK() {
	want := &domain.Note{
		ID: "abc-123", Title: "a",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(want, nil)

	note, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading() {
	note := &domain.Note{
		ID: "abc-123", Title: "a", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_BurnAfterReading_WithTTL() {
	note := &domain.Note{
		ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 3600,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).Return(note, nil)

	result, err := s.manager.Get(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestGet_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "abc-123", Title: "a", ExpiresAt: &past,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	_, err := s.manager.Get(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestGet_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Get(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// View

func (s *ManagerTestSuite) TestView_OK() {
	want := &domain.Note{
		ID: "abc-123", Title: "a", Views: 5,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		EditCode: "", BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(want, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(6, note.Views)
}

func (s *ManagerTestSuite) TestView_BurnAfterReading() {
	want := &domain.Note{
		ID: "abc-123", Title: "a", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(want, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("abc-123")).Return(want, nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(want, note)
	s.Equal(0, note.Views) // no increment for burn-after-reading
}

func (s *ManagerTestSuite) TestView_BurnAfterReading_WithTTL() {
	future := time.Now().Add(30 * time.Minute)
	stored := &domain.Note{
		ID: "abc-123", Title: "a", BurnAfterReading: true, BurnTTL: 1800,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0,
		ClearExpiry: false,
	}
	// Real storage flips burn_after_reading=false and sets expires_at on the timer-start read.
	afterBurn := &domain.Note{
		ID: "abc-123", Title: "a", BurnAfterReading: false, BurnTTL: 1800, ExpiresAt: &future,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil,
		Content: "", ContentType: nil, EditCode: "", Views: 0, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(stored, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).Return(afterBurn, nil)
	// The note stays readable during the grace period, so each read counts.
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	note, err := s.manager.View(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.False(note.BurnAfterReading)
	s.Equal(1, note.Views)
}

func (s *ManagerTestSuite) TestView_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("missing")).Return(nil, domain.ErrNotFound)

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

		UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		Content: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	note := &domain.Note{
		Title: "updated", Content: "body",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", uuid.Nil, note)

	s.Require().NoError(err)
	s.Equal("abc-123", result.ID)
	s.False(result.UpdatedAt.IsZero())
	s.Equal(existing.CreatedAt, result.CreatedAt)
	s.Equal(existing.ContentType, result.ContentType)
	s.Equal("secret123456", result.EditCode)
}

// TestUpdate_ResponseReportsPersistedPrivacyAndViews verifies the PUT response backfills
// privacy (when the caller omitted it) and views from the existing note — the repository's
// UPDATE persists privacy via COALESCE and never writes views, so the response must report the
// stored state rather than the caller's zero-value defaults (a privacy=owner note would
// otherwise answer "public" with "views": 0, contradicting the next GET).
func (s *ManagerTestSuite) TestUpdate_ResponseReportsPersistedPrivacyAndViews() {
	ownerPrivacy := domain.PrivacyOwner
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		Privacy:  &ownerPrivacy,
		Views:    7,
		OwnerID:  new(uuid.UUID),
		EditCode: "secret123456",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Content: "", ContentType: nil, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	// Caller omits privacy; it must be inherited, not defaulted to public.
	note := &domain.Note{
		Title: "updated", Content: "body",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", *existing.OwnerID, note)

	s.Require().NoError(err)
	s.Equal(ownerPrivacy, *result.Privacy)
	s.Equal(7, result.Views)

	// Explicit privacy still wins over the stored value.
	newPrivacy := domain.PrivacyAuthenticated
	explicit := &domain.Note{
		Title: "updated", Privacy: &newPrivacy,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), explicit).Return(nil)

	result, err = s.manager.Update(s.T().Context(), "abc-123", "secret123456", *existing.OwnerID, explicit)

	s.Require().NoError(err)
	s.Equal(newPrivacy, *result.Privacy)
}

func (s *ManagerTestSuite) TestUpdate_EmptyTitle() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "original",
		Content:  "original body",
		EditCode: "code",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "code", uuid.Nil, &domain.Note{
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		Content: "", ContentType: nil, EditCode: "", Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	})

	s.Require().NoError(err)
	s.NotNil(result)
}

func (s *ManagerTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{
		Title:     "updated",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("missing")).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), "missing", "code", uuid.Nil, note)

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestUpdate_Forbidden() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title:     "updated",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "abc-123", "wrong-code", uuid.Nil, note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// TestUpdate_OwnerBypassesEditCode verifies that the exact user who created the note (callerID
// matches existing.OwnerID) may update it with an empty or wrong edit_code.
func (s *ManagerTestSuite) TestUpdate_OwnerBypassesEditCode() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",
		OwnerID:  new(testUserID1),

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, Content: "", ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title: "updated", Content: "body",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "abc-123", "", testUserID1, note)

	s.Require().NoError(err)
	s.Equal("updated", result.Title)
}

// TestUpdate_NonOwnerCallerStillNeedsEditCode verifies that an authenticated caller who is NOT
// the note's owner does not bypass edit_code — the owner check never extends to other users.
func (s *ManagerTestSuite) TestUpdate_NonOwnerCallerStillNeedsEditCode() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",
		OwnerID:  new(testUserID1),

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, Content: "", ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title:     "hijacked",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "abc-123", "wrong-code", testUserID2, note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// TestUpdate_AnonymousNoteAlwaysNeedsEditCode verifies that a note with no owner (created
// anonymously, or accounts disabled) never bypasses edit_code, even for an authenticated caller.
func (s *ManagerTestSuite) TestUpdate_AnonymousNoteAlwaysNeedsEditCode() {
	existing := &domain.Note{
		ID:       "abc-123",
		Title:    "old",
		EditCode: "secret123456",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title:     "hijacked",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "abc-123", "", testUserID1, note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// Delete

func (s *ManagerTestSuite) TestDelete_OK() {
	existing := &domain.Note{
		ID: "abc-123", EditCode: "secret123456",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", Content: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "secret123456", uuid.Nil)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("missing")).Return(nil, domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing", "code", uuid.Nil)

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestDelete_Forbidden() {
	existing := &domain.Note{
		ID: "abc-123", EditCode: "secret123456",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", Content: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "wrong-code", uuid.Nil)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// TestDelete_OwnerBypassesEditCode mirrors TestUpdate_OwnerBypassesEditCode for Delete.
func (s *ManagerTestSuite) TestDelete_OwnerBypassesEditCode() {
	existing := &domain.Note{
		ID: "abc-123", EditCode: "secret123456", OwnerID: new(testUserID1),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, Title: "", Content: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "", testUserID1)

	s.Require().NoError(err)
}

// TestDelete_NonOwnerCallerStillNeedsEditCode mirrors TestUpdate_NonOwnerCallerStillNeedsEditCode
// for Delete.
func (s *ManagerTestSuite) TestDelete_NonOwnerCallerStillNeedsEditCode() {
	existing := &domain.Note{
		ID: "abc-123", EditCode: "secret123456", OwnerID: new(testUserID1),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, Title: "", Content: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "abc-123", "wrong-code", testUserID2)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// GetRendered

func (s *ManagerTestSuite) TestGetRendered_OK() {
	note := &domain.Note{
		ID: "abc-123", Content: "# Hello",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", html)
}

// ── content size cap ──

func (s *ManagerTestSuite) TestCreate_ContentOverCap_Rejected() {
	note := &domain.Note{
		Content:   strings.Repeat("a", domain.MaxContentLength+1),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.ErrorIs(err, domain.ErrContentTooLarge)
}

func (s *ManagerTestSuite) TestCreate_ContentAtCap_Accepted() {
	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	note := &domain.Note{
		Content:   strings.Repeat("a", domain.MaxContentLength),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	_, err := s.manager.Create(s.T().Context(), note)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestUpdate_ContentOverCap_Rejected() {
	note := &domain.Note{
		Content:   strings.Repeat("é", domain.MaxContentLength+1),
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	_, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", uuid.Nil, note)

	s.ErrorIs(err, domain.ErrContentTooLarge)
}

// ── render cache ──

// statefulRenderCache backs the generated MockRenderCache with a map so the tests observe real
// get/set/delete behaviour rather than a script of calls.
func (s *ManagerTestSuite) statefulRenderCache() *MockRenderCache {
	type entry struct {
		updatedAt time.Time
		html      string
	}

	store := map[string]entry{}
	cache := NewMockRenderCache(s.ctrl)

	cache.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(id string, updatedAt time.Time) (string, bool) {
			cached, ok := store[id]
			if !ok || !cached.updatedAt.Equal(updatedAt) {
				return "", false
			}

			return cached.html, true
		}).AnyTimes()
	cache.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(id string, updatedAt time.Time, html string) { store[id] = entry{updatedAt, html} }).AnyTimes()
	cache.EXPECT().Delete(gomock.Any()).DoAndReturn(func(id string) { delete(store, id) }).AnyTimes()

	return cache
}

// TestGetRendered_UnchangedNoteIsRenderedOnce: two views of an unchanged note cost one render.
func (s *ManagerTestSuite) TestGetRendered_UnchangedNoteIsRenderedOnce() {
	s.manager.WithRenderCache(s.statefulRenderCache())

	stamp := time.Now().Add(-time.Hour)

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).
		DoAndReturn(func(context.Context, string) (*domain.Note, error) {
			return &domain.Note{
				ID: "abc-123", Content: "# Hello", UpdatedAt: stamp,
				CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
				Title: "", ContentType: nil, EditCode: "", Views: 0,
				BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
			}, nil
		}).Times(2)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil).Times(2)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil).Times(1)

	for range 2 {
		_, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")
		s.Require().NoError(err)
		s.Equal("<h1>Hello</h1>", html)
	}
}

// TestGetRendered_ReRendersAfterUpdate: Update invalidates the entry, so the next view renders
// the new content instead of serving stale HTML.
func (s *ManagerTestSuite) TestGetRendered_ReRendersAfterUpdate() {
	s.manager.WithRenderCache(s.statefulRenderCache())

	stamp := time.Now().Add(-time.Hour)
	current := &domain.Note{
		ID: "abc-123", Content: "old", UpdatedAt: stamp, EditCode: "secret123456",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		Title: "", ContentType: nil, Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).
		DoAndReturn(func(context.Context, string) (*domain.Note, error) {
			clone := *current

			return &clone, nil
		}).AnyTimes()
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ *domain.Note) error {
			// Stored state after the update; keep the old UpdatedAt to prove the explicit
			// invalidation (not just the changed key) drops the entry.
			current = &domain.Note{
				ID: "abc-123", Content: "new", UpdatedAt: stamp, EditCode: "secret123456",
				CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
				Title: "", ContentType: nil, Views: 0, BurnTTL: 0,
				BurnAfterReading: false, ClearExpiry: false,
			}

			return nil
		})
	s.renderer.EXPECT().Render("old").Return("<p>old</p>", nil).Times(1)
	s.renderer.EXPECT().Render("new").Return("<p>new</p>", nil).Times(1)

	_, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")
	s.Require().NoError(err)
	s.Equal("<p>old</p>", html)

	_, err = s.manager.Update(s.T().Context(), "abc-123", "secret123456", uuid.Nil, &domain.Note{
		Content:   "new",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	})
	s.Require().NoError(err)

	_, html, err = s.manager.GetRendered(s.T().Context(), "abc-123")
	s.Require().NoError(err)
	s.Equal("<p>new</p>", html)
}

// TestGetRendered_ReRendersWhenUpdatedAtChanges covers an update made through another replica:
// no local invalidation happens, but the new updated_at misses the cached entry.
func (s *ManagerTestSuite) TestGetRendered_ReRendersWhenUpdatedAtChanges() {
	s.manager.WithRenderCache(s.statefulRenderCache())

	first := &domain.Note{
		ID: "abc-123", Content: "v1", UpdatedAt: time.Now().Add(-time.Hour),
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		Title: "", ContentType: nil, EditCode: "", Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	second := &domain.Note{
		ID: "abc-123", Content: "v2", UpdatedAt: time.Now(),
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		Title: "", ContentType: nil, EditCode: "", Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	gomock.InOrder(
		s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(first, nil),
		s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(second, nil),
	)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	s.renderer.EXPECT().Render("v1").Return("<p>v1</p>", nil)
	s.renderer.EXPECT().Render("v2").Return("<p>v2</p>", nil)

	_, html, err := s.manager.GetRendered(s.T().Context(), "abc-123")
	s.Require().NoError(err)
	s.Equal("<p>v1</p>", html)

	_, html, err = s.manager.GetRendered(s.T().Context(), "abc-123")
	s.Require().NoError(err)
	s.Equal("<p>v2</p>", html)
}

// TestGetRendered_BurnNotesAreNeverCached: reading a burn-after-reading note destroys it, and a
// cached copy would outlive the burn in memory.
func (s *ManagerTestSuite) TestGetRendered_BurnNotesAreNeverCached() {
	cache := NewMockRenderCache(s.ctrl)
	cache.EXPECT().Get(gomock.Any(), gomock.Any()).Times(0)
	cache.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	s.manager.WithRenderCache(cache)

	note := &domain.Note{
		ID: "abc-123", Content: "# Hello", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Title: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
}

// TestDelete_DropsCachedRender: a deleted note's rendered content must not linger in memory.
func (s *ManagerTestSuite) TestDelete_DropsCachedRender() {
	cache := NewMockRenderCache(s.ctrl)
	s.manager.WithRenderCache(cache)

	existing := &domain.Note{
		ID: "abc-123", EditCode: "secret123456",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", Content: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)
	cache.EXPECT().Delete(domain.HashSlug("abc-123"))

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc-123", "secret123456", uuid.Nil))
}

func (s *ManagerTestSuite) TestGetRendered_BurnAfterReading() {
	note := &domain.Note{
		ID: "abc-123", Content: "# Hello", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Title: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, rendered, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", rendered)
}

func (s *ManagerTestSuite) TestGetRendered_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "abc-123", Content: "# Hello", ExpiresAt: &past,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Title: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestGetRendered_StorageError() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(nil, domain.ErrNotFound)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestGetRendered_RenderError() {
	renderErr := errors.New("render failed")
	note := &domain.Note{
		ID: "abc-123", Content: "bad",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().Render("bad").Return("", renderErr)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().ErrorIs(err, renderErr)
	// The error is logged server-side: it names the note by hash, never by its capability slug.
	s.NotContains(err.Error(), "abc-123")
	s.Contains(err.Error(), domain.HashSlug("abc-123"))
}

func (s *ManagerTestSuite) TestGetRendered_PlainText_UsesRenderPlain() {
	plain := domain.ContentTypePlain
	note := &domain.Note{
		ID: "abc-123", Content: "<script>", ContentType: &plain,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().RenderPlain("<script>").Return("<pre>&lt;script&gt;</pre>", nil)

	result, rendered, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<pre>&lt;script&gt;</pre>", rendered)
}

func (s *ManagerTestSuite) TestGetRendered_PlainText_RenderPlainError() {
	renderErr := errors.New("render failed")
	plain := domain.ContentTypePlain
	note := &domain.Note{
		ID: "abc-123", Content: "text", ContentType: &plain,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("abc-123")).Return(nil)
	s.renderer.EXPECT().RenderPlain("text").Return("", renderErr)

	_, _, err := s.manager.GetRendered(s.T().Context(), "abc-123")

	s.Require().ErrorIs(err, renderErr)
	s.NotContains(err.Error(), "abc-123")
	s.Contains(err.Error(), domain.HashSlug("abc-123"))
}

// Peek

func (s *ManagerTestSuite) TestPeek_OK() {
	note := &domain.Note{
		ID: "peek-id", Title: "t", Content: "c",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("peek-id")).Return(note, nil)

	result, err := s.manager.Peek(s.T().Context(), "peek-id")

	s.Require().NoError(err)
	s.Equal(note, result)
}

func (s *ManagerTestSuite) TestPeek_NotFound() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("missing")).Return(nil, domain.ErrNotFound)

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
	note := &domain.Note{
		ID: "ttl-note", Title: "t", Views: 0, ExpiresAt: &future,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "",
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	// First view
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("ttl-note")).Return(note, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("ttl-note")).Return(nil)

	result, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(1, result.Views)

	// Second view — must NOT return ErrNotFound
	note2 := &domain.Note{
		ID: "ttl-note", Title: "t", Views: 1, ExpiresAt: &future,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "",
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("ttl-note")).Return(note2, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("ttl-note")).Return(nil)

	result2, err := s.manager.View(s.T().Context(), "ttl-note")
	s.Require().NoError(err)
	s.Equal(2, result2.Views)
}

// TestView_BurnAfterReading_NoIncrementViews verifies that burn-after-reading notes
// are consumed (deleted) and IncrementViews is NOT called on the already-deleted note.
func (s *ManagerTestSuite) TestView_BurnAfterReading_NoIncrementViews() {
	note := &domain.Note{
		ID: "burn-note", Title: "t", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-note")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("burn-note")).Return(note, nil)
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
	noteAfterBurn := &domain.Note{
		ID: "burn-ttl", Title: "t", BurnAfterReading: false, BurnTTL: 3600, ExpiresAt: &future,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil,
		Content: "", ContentType: nil, EditCode: "", Views: 0, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-ttl")).
		Return(&domain.Note{
			ID: "burn-ttl", Title: "t", BurnAfterReading: true, BurnTTL: 3600,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
			OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0,
			ClearExpiry: false,
		}, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), domain.HashSlug("burn-ttl"), gomock.Any()).Return(noteAfterBurn, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("burn-ttl")).Return(nil)

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
	note := &domain.Note{
		ID: "burn-ttl-race", Title: "t", BurnAfterReading: true, BurnTTL: 3600,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0,
		ClearExpiry: false,
	}

	// Request B's perspective: it read the note before A's SetBurnExpiry committed.
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-ttl-race")).Return(note, nil)
	// A already flipped burn_after_reading=false; B's conditional UPDATE matches 0 rows.
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), domain.HashSlug("burn-ttl-race"), gomock.Any()).
		Return(nil, domain.ErrNotFound)

	// The fix must re-fetch the note to return its current state (BurnAfterReading=false, ExpiresAt set).
	future := time.Now().Add(time.Hour)
	noteAfterBurn := &domain.Note{
		ID:               "burn-ttl-race",
		Title:            "t",
		BurnAfterReading: false,
		BurnTTL:          3600,
		ExpiresAt:        &future,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil,
		Content: "", ContentType: nil, EditCode: "", Views: 0, ClearExpiry: false,
	}
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-ttl-race")).Return(noteAfterBurn, nil)

	// The note exists and its burn timer is running — Get must succeed, not return ErrNotFound.
	result, err := s.manager.Get(s.T().Context(), "burn-ttl-race")
	s.Require().NoError(err)
	s.NotNil(result)
	s.False(result.BurnAfterReading)
	s.NotNil(result.ExpiresAt)
}

// ViewPreloaded

func (s *ManagerTestSuite) TestViewPreloaded_OK() {
	note := &domain.Note{
		ID: "pre-1", Title: "a", Views: 2,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		EditCode: "", BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("pre-1")).Return(nil)

	result, err := s.manager.ViewPreloaded(s.T().Context(), "pre-1", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal(3, result.Views)
}

func (s *ManagerTestSuite) TestViewPreloaded_BurnAfterReading() {
	note := &domain.Note{
		ID: "pre-2", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Title: "", Content: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, ClearExpiry: false,
	}
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("pre-2")).Return(note, nil)

	result, err := s.manager.ViewPreloaded(s.T().Context(), "pre-2", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal(0, result.Views)
}

func (s *ManagerTestSuite) TestViewPreloaded_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "pre-3", ExpiresAt: &past,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Title: "", Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("pre-3")).Return(nil)

	_, err := s.manager.ViewPreloaded(s.T().Context(), "pre-3", note)

	s.ErrorIs(err, domain.ErrExpired)
}

// EnforceExpiry — expiry only, never burn

func (s *ManagerTestSuite) TestEnforceExpiry_ExpiredNoteIsDeleted() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "ee-1", ExpiresAt: &past, BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil,
		Title: "", Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("ee-1")).Return(nil)

	err := s.manager.EnforceExpiry(s.T().Context(), "ee-1", note)

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestEnforceExpiry_LiveBurnNoteIsNotConsumed() {
	future := time.Now().Add(time.Hour)

	for _, note := range []*domain.Note{
		{
			ID: "ee-2", BurnAfterReading: true,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
			OwnerID: nil, Title: "", Content: "", ContentType: nil, EditCode: "",
			Views: 0, BurnTTL: 0, ClearExpiry: false,
		},
		{
			ID: "ee-3", BurnAfterReading: true, BurnTTL: 60,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
			OwnerID: nil, Title: "", Content: "", ContentType: nil, EditCode: "",
			Views: 0, ClearExpiry: false,
		},
		{
			ID: "ee-4", ExpiresAt: &future,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
			OwnerID: nil, Title: "", Content: "", ContentType: nil,
			EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
			ClearExpiry: false,
		},
	} {
		// No storage call at all: no Consume, no SetBurnExpiry, no Delete, no IncrementViews.
		err := s.manager.EnforceExpiry(s.T().Context(), note.ID, note)

		s.Require().NoError(err, note.ID)
	}
}

// GetRenderedPreloaded

func (s *ManagerTestSuite) TestGetRenderedPreloaded_OK() {
	note := &domain.Note{
		ID: "rp-1", Content: "# Hello",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().IncrementViews(gomock.Any(), domain.HashSlug("rp-1")).Return(nil)
	s.renderer.EXPECT().Render("# Hello").Return("<h1>Hello</h1>", nil)

	result, rendered, err := s.manager.GetRenderedPreloaded(s.T().Context(), "rp-1", note)

	s.Require().NoError(err)
	s.Equal(note, result)
	s.Equal("<h1>Hello</h1>", rendered)
}

func (s *ManagerTestSuite) TestGetRenderedPreloaded_Expired() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "rp-2", ExpiresAt: &past, Content: "x",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Title: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("rp-2")).Return(nil)

	_, _, err := s.manager.GetRenderedPreloaded(s.T().Context(), "rp-2", note)

	s.ErrorIs(err, domain.ErrExpired)
}

// Peek — expiry policy is intentionally NOT applied

// TestPeek_ReturnsExpiredNote documents that Peek bypasses expiry policy by design.
// Callers (e.g. auth middleware) use Peek to inspect note metadata without side effects;
// they must not trigger deletion or policy enforcement.
func (s *ManagerTestSuite) TestPeek_ReturnsExpiredNote() {
	past := time.Now().Add(-time.Minute)
	note := &domain.Note{
		ID: "expired-peek", Title: "t", ExpiresAt: &past,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("expired-peek")).Return(note, nil)
	// Delete must NOT be called — Peek does not enforce expiry.

	result, err := s.manager.Peek(s.T().Context(), "expired-peek")

	s.Require().NoError(err)
	s.Equal(note, result)
}

// Update — private notes

func (s *ManagerTestSuite) TestUpdate_PrivateNote_CorrectCode() {
	priv := domain.PrivacyAuthenticated
	existing := &domain.Note{
		ID:       "priv-1",
		Title:    "secret",
		EditCode: "rightcode1234",
		Privacy:  &priv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, Content: "", ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title:     "updated secret",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("priv-1")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("priv-1"), note).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "priv-1", "rightcode1234", uuid.Nil, note)

	s.Require().NoError(err)
	s.Equal("updated secret", result.Title)
}

// TestUpdate_OwnerPrivacy_AnonymousExistingNote_Rejected verifies that a caller with a valid
// edit_code cannot flip an anonymously-created (ownerless) note to privacy=owner — such a note
// could never be read back by anyone afterward.
func (s *ManagerTestSuite) TestUpdate_OwnerPrivacy_AnonymousExistingNote_Rejected() {
	priv := domain.PrivacyOwner
	existing := &domain.Note{
		ID:       "anon-1",
		Title:    "old",
		EditCode: "rightcode1234",

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Content: "", ContentType: nil,
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title: "updated", Privacy: &priv,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("anon-1")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "anon-1", "rightcode1234", uuid.Nil, note)

	s.ErrorIs(err, domain.ErrOwnerPrivacyRequiresOwner)
}

// TestUpdate_OwnerPrivacy_AlreadyOwnedNote_OK verifies that setting privacy=owner on a note that
// already has a real owner is accepted.
func (s *ManagerTestSuite) TestUpdate_OwnerPrivacy_AlreadyOwnedNote_OK() {
	priv := domain.PrivacyOwner
	existing := &domain.Note{
		ID:       "owned-1",
		Title:    "old",
		EditCode: "rightcode1234",
		OwnerID:  new(testUserID1),

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, Content: "", ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title: "updated", Privacy: &priv,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", Content: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("owned-1")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("owned-1"), gomock.Any()).Return(nil)

	result, err := s.manager.Update(s.T().Context(), "owned-1", "rightcode1234", uuid.Nil, note)

	s.Require().NoError(err)
	s.Equal(domain.PrivacyOwner, result.EffectivePrivacy())
}

func (s *ManagerTestSuite) TestUpdate_PrivateNote_WrongCode() {
	priv := domain.PrivacyAuthenticated
	existing := &domain.Note{
		ID:       "priv-2",
		Title:    "secret",
		EditCode: "rightcode1234",
		Privacy:  &priv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, Content: "", ContentType: nil, Views: 0,
		BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	note := &domain.Note{
		Title:     "hijacked",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Content: "",
		ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		BurnAfterReading: false, ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("priv-2")).Return(existing, nil)

	_, err := s.manager.Update(s.T().Context(), "priv-2", "wrongcode0000", uuid.Nil, note)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

// Consume error path

func (s *ManagerTestSuite) TestGet_BurnAfterReading_ConsumeError() {
	consumeErr := errors.New("storage unavailable")
	note := &domain.Note{
		ID: "burn-err", Title: "t", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-err")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("burn-err")).Return(nil, consumeErr)

	_, err := s.manager.Get(s.T().Context(), "burn-err")

	s.ErrorIs(err, consumeErr)
}

func (s *ManagerTestSuite) TestView_BurnAfterReading_ConsumeError() {
	consumeErr := errors.New("storage unavailable")
	note := &domain.Note{
		ID: "burn-err-view", Title: "t", BurnAfterReading: true,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, Content: "", ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-err-view")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("burn-err-view")).Return(nil, consumeErr)

	_, err := s.manager.View(s.T().Context(), "burn-err-view")

	s.ErrorIs(err, consumeErr)
}

// Decryption failure

func TestGet_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	links := NewMockLinksChecker(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, links, noopSlugLock{}, renderer, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)

	note := &domain.Note{
		ID: "enc-fail", Title: "t", Content: "ciphertext",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	storage.EXPECT().Get(gomock.Any(), domain.HashSlug("enc-fail")).Return(note, nil)

	_, err := mgr.Get(t.Context(), "enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPeek_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	links := NewMockLinksChecker(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, links, noopSlugLock{}, renderer, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)

	note := &domain.Note{
		ID: "enc-fail-peek", Title: "t", Content: "ciphertext",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	}
	storage.EXPECT().Get(gomock.Any(), domain.HashSlug("enc-fail-peek")).Return(note, nil)

	_, err := mgr.Peek(t.Context(), "enc-fail-peek")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestGet_BurnAfterReading_DecryptionFailure verifies that decryption failure on the initial
// storage.Get result propagates as ErrDecryptionFailed before Consume is ever called.
func TestGet_BurnAfterReading_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	links := NewMockLinksChecker(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, links, noopSlugLock{}, renderer, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)

	// decryptContent runs before burn.applyPolicy, so Consume is never reached.
	raw := &domain.Note{
		ID: "burn-enc-fail", Title: "t", BurnAfterReading: true, Content: "ciphertext",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
		ClearExpiry: false,
	}
	storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-enc-fail")).Return(raw, nil)

	_, err := mgr.Get(t.Context(), "burn-enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestGet_BurnTTL_DecryptionFailure verifies that decryption failure on the initial
// storage.Get result propagates as ErrDecryptionFailed before SetBurnExpiry is ever called.
func TestGet_BurnTTL_DecryptionFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	storage := NewMockStorage(ctrl)
	links := NewMockLinksChecker(ctrl)
	renderer := NewMockRenderer(ctrl)
	mgr := NewManager(storage, links, noopSlugLock{}, renderer, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)

	// decryptContent runs before burn.applyPolicy, so SetBurnExpiry is never reached.
	raw := &domain.Note{
		ID: "burn-ttl-enc-fail", Title: "t", BurnAfterReading: true, BurnTTL: 3600, Content: "ciphertext",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
		OwnerID: nil, ContentType: nil, EditCode: "", Views: 0, ClearExpiry: false,
	}
	storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-ttl-enc-fail")).Return(raw, nil)

	_, err := mgr.Get(t.Context(), "burn-ttl-enc-fail")

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestUpdateExplicitExpiry() {
	future := time.Now().Add(time.Hour)
	for _, testCase := range []struct {
		name   string
		update *domain.Note
		want   *time.Time
	}{
		{"preserve", &domain.Note{
			Content:   "body",
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, ID: "", Title: "",
			ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
			BurnAfterReading: false, ClearExpiry: false,
		}, &future},
		{"clear", &domain.Note{
			Content: "body", ClearExpiry: true,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, ID: "", Title: "",
			ContentType: nil, EditCode: "", Views: 0, BurnTTL: 0,
			BurnAfterReading: false,
		}, nil},
		{"replace", &domain.Note{
			Content: "body", ExpiresAt: &future,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
			OwnerID: nil, ID: "", Title: "", ContentType: nil,
			EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
			ClearExpiry: false,
		}, &future},
	} {
		s.Run(testCase.name, func() {
			existing := &domain.Note{
				ID: domain.HashSlug("slug"), EditCode: "code", ExpiresAt: &future,
				CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
				OwnerID: nil, Title: "", Content: "", ContentType: nil,
				Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
			}
			s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("slug")).Return(existing, nil)
			s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("slug"), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, note *domain.Note) error {
					s.Equal(testCase.want, note.ExpiresAt)

					return nil
				})
			updated, err := s.manager.Update(s.T().Context(), "slug", "code", uuid.Nil, testCase.update)
			s.Require().NoError(err)
			s.Equal(testCase.want, updated.ExpiresAt)
		})
	}
}

// TestUpdateBurnImmediateExpiry pins resolveExpiryTime for the payload the HTTP adapter builds
// from UpdateNoteRequest with burn_after_reading=true and ttl omitted, null or 0 (all three map
// to BurnTTL=0, no ExpiresAt, no ClearExpiry): an existing hard expiry is kept, while a stale
// burn-timer expiry from a previous burn TTL is dropped.
func (s *ManagerTestSuite) TestUpdateBurnImmediateExpiry() {
	future := time.Now().Add(time.Hour)
	for _, testCase := range []struct {
		name     string
		existing *domain.Note
		want     *time.Time
	}{
		{"hard expiry kept", &domain.Note{
			EditCode: "code", ExpiresAt: &future,
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil,
			OwnerID: nil, ID: "", Title: "", Content: "",
			ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
			ClearExpiry: false,
		}, &future},
		{"no expiry stays none", &domain.Note{
			EditCode:  "code",
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, ID: "", Title: "",
			Content: "", ContentType: nil, Views: 0, BurnTTL: 0,
			BurnAfterReading: false, ClearExpiry: false,
		}, nil},
		{
			"stale burn timer dropped",
			&domain.Note{
				EditCode: "code", BurnAfterReading: true, BurnTTL: 600, ExpiresAt: &future,
				CreatedAt: time.Time{}, UpdatedAt: time.Time{}, Privacy: nil, OwnerID: nil,
				ID: "", Title: "", Content: "", ContentType: nil, Views: 0,
				ClearExpiry: false,
			},
			nil,
		},
	} {
		s.Run(testCase.name, func() {
			s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("slug")).Return(testCase.existing, nil)
			s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("slug"), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, note *domain.Note) error {
					s.Equal(testCase.want, note.ExpiresAt)

					return nil
				})

			update := &domain.Note{
				Content: "body", BurnAfterReading: true,
				CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil,
				OwnerID: nil, ID: "", Title: "", ContentType: nil, EditCode: "",
				Views: 0, BurnTTL: 0, ClearExpiry: false,
			}
			_, err := s.manager.Update(s.T().Context(), "slug", "code", uuid.Nil, update)
			s.Require().NoError(err)
		})
	}
}

func (s *ManagerTestSuite) TestDecryptionFailureLogsOnlyHashedSlug() {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: false, ReplaceAttr: nil,
	}))
	note := &domain.Note{
		ID: "secret-slug", Content: "invalid",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}
	err := decryptContent(s.T().Context(), failingEncryptor{}, log, note)
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Contains(buf.String(), domain.HashSlug(note.ID))
	s.NotContains(buf.String(), note.ID)
}
