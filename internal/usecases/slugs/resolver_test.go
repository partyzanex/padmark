package slugs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type ResolverSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	storage  *MockStorage
	resolver *Resolver
}

func TestResolverSuite(t *testing.T) {
	suite.Run(t, new(ResolverSuite))
}

func (s *ResolverSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.resolver = NewResolver(s.storage)
}

func (s *ResolverSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestKindOf_ReturnsStoredKind checks that KindOf reads the row by the hashed slug and returns
// each of the three kinds.
func (s *ResolverSuite) TestKindOf_ReturnsStoredKind() {
	for _, kind := range []Kind{KindNote, KindLink, KindFile} {
		s.Run(string(kind), func() {
			ctx := s.T().Context()
			s.storage.EXPECT().Kind(ctx, domain.HashSlug("abc")).Return(kind, nil)

			got, err := s.resolver.KindOf(ctx, "abc")
			s.Require().NoError(err)
			s.Equal(kind, got)
		})
	}
}

// TestKindOf_MissingSlug_ReturnsErrNotFound checks that a slug without a row gives
// domain.ErrNotFound through the wrapped error.
func (s *ResolverSuite) TestKindOf_MissingSlug_ReturnsErrNotFound() {
	ctx := s.T().Context()
	s.storage.EXPECT().Kind(ctx, domain.HashSlug("abc")).Return(Kind(""), domain.ErrNotFound)

	_, err := s.resolver.KindOf(ctx, "abc")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestKindOf_StorageError_IsReturned checks that a failed read is returned (fail-hard).
func (s *ResolverSuite) TestKindOf_StorageError_IsReturned() {
	ctx := s.T().Context()
	storageErr := errors.New("database is closed")
	s.storage.EXPECT().Kind(ctx, domain.HashSlug("abc")).Return(Kind(""), storageErr)

	_, err := s.resolver.KindOf(ctx, "abc")
	s.Require().ErrorIs(err, storageErr)
}

// TestKindOf_UnknownKind_ReturnsError checks that a resource name of a newer version is an
// error and not a kind that the caller cannot serve.
func (s *ResolverSuite) TestKindOf_UnknownKind_ReturnsError() {
	ctx := s.T().Context()
	s.storage.EXPECT().Kind(ctx, domain.HashSlug("abc")).Return(Kind("videos"), nil)

	kind, err := s.resolver.KindOf(ctx, "abc")
	s.Require().ErrorIs(err, errUnknownKind)
	s.Empty(kind)
}

// TestKindOf_MultiSegmentSlug_IsNoteWithoutRead checks that a slug with "/" is a note without a
// namespace read: the strict mock fails on any Kind call.
func (s *ResolverSuite) TestKindOf_MultiSegmentSlug_IsNoteWithoutRead() {
	kind, err := s.resolver.KindOf(s.T().Context(), "project/guide.md")
	s.Require().NoError(err)
	s.Equal(KindNote, kind)
}
