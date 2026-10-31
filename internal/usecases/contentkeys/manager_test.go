package contentkeys

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	activeKey      = "key"
	spareKey       = "spare"
	installationID = "1b4e28ba-2fa1-11d2-883f-0016d3cca427"
	// storedHash stands for a hashed edit code: every note written since v0.2.0 stores one, and
	// the hasher mock below treats only this value as hash-shaped.
	storedHash = "s1$stored$hash"

	legacyValue   = "bGVnYWN5LWNpcGhlcnRleHQtYmFzZTY0"
	corruptValue  = "plaintext from before encryption"
	dollarValue   = "costs $5; see $HOME and $x^2$"
	nativeValue   = "pmc2$p$key$wrapped-dek$payload"
	wrappedValue  = "pmc2$l$key$wrapped-dek$payload"
	spareValue    = "pmc2$p$spare$wrapped-dek$payload"
	futureValue   = "pmc9$x$y$z$w"
	garbageValue  = "pmc2$garbage"
	wrongKeyValue = "pmc2$p$key$foreign-dek$payload"
)

var (
	errKeyMismatch = errors.New("content key does not match this installation")
	errMalformed   = errors.New("malformed ciphertext")
	errDecryption  = errors.New("decryption failed")
)

// cipherFixture describes how the mocked Cipher treats one stored value; the real
// classification and authentication are covered by internal/infra/crypto tests.
type cipherFixture struct {
	versioned  bool
	legacyErr  error
	kind, key  string
	inspectErr error
}

//nolint:gochecknoglobals // immutable test fixture table
var fixtures = map[string]cipherFixture{
	legacyValue: {
		versioned: false, legacyErr: nil, kind: "", key: "", inspectErr: nil,
	},
	corruptValue: {
		legacyErr: errMalformed,
		versioned: false, kind: "", key: "", inspectErr: nil,
	},
	dollarValue: {
		legacyErr: errMalformed,
		versioned: false, kind: "", key: "", inspectErr: nil,
	},
	nativeValue: {
		versioned: true, kind: "p", key: activeKey,
		legacyErr: nil, inspectErr: nil,
	},
	wrappedValue: {
		versioned: true, kind: "l", key: activeKey,
		legacyErr: nil, inspectErr: nil,
	},
	spareValue: {
		versioned: true, kind: "p", key: spareKey,
		legacyErr: nil, inspectErr: nil,
	},
	futureValue: {
		versioned: true, inspectErr: errMalformed,
		legacyErr: nil, kind: "", key: "",
	},
	garbageValue: {
		versioned: true, inspectErr: errMalformed,
		legacyErr: nil, kind: "", key: "",
	},
	wrongKeyValue: {
		versioned: true, inspectErr: errDecryption,
		legacyErr: nil, kind: "", key: "",
	},
}

func checkFor(keyID string) string { return "check-" + keyID }

// newRow builds a record as a release since v0.2.0 stores it: notes carry a hashed edit code.
func newRow(resource, slug, value string) Row {
	row := Row{
		Resource: resource, ID: domain.HashSlug(slug), Value: value,
		Legacy: nil, StoredEditCode: "",
	}
	if resource == resourceNote {
		row.StoredEditCode = storedHash
	}

	return row
}

func readyRegistry() *Registry {
	return &Registry{InstallationID: installationID, Phase: Ready, Checks: map[string]string{
		activeKey: checkFor(activeKey),
	}}
}

// pageRows serves Batch from the test's current rows, ordered by ID per resource.
func pageRows(rows *[]Row) func(context.Context, string, string, int) ([]Row, error) {
	return func(_ context.Context, resource, after string, limit int) ([]Row, error) {
		var page []Row

		for _, row := range *rows {
			if row.Resource == resource && row.ID > after {
				page = append(page, row)
			}
		}

		slices.SortFunc(page, func(a, b Row) int { return strings.Compare(a.ID, b.ID) })

		return page[:min(len(page), limit)], nil
	}
}

func countPrefix(err error) int {
	return strings.Count(err.Error(), errorContext+":")
}

type ManagerTestSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	store       *MockStorage
	keys        *MockKeyring
	notes       *MockCipher
	links       *MockCipher
	hasher      *MockEditCodeHasher
	manager     *Manager
	keyIDs      []string
	substituted map[string]bool
}

func (s *ManagerTestSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.store = NewMockStorage(s.ctrl)
	s.keys = NewMockKeyring(s.ctrl)
	s.notes = NewMockCipher(s.ctrl)
	s.links = NewMockCipher(s.ctrl)
	s.keyIDs = []string{activeKey}
	s.substituted = make(map[string]bool)
	s.stubKeyring()
	s.stubCipher(s.notes)
	s.stubCipher(s.links)
	s.hasher = NewMockEditCodeHasher(s.ctrl)
	s.hasher.EXPECT().IsStoredHash(gomock.Any()).DoAndReturn(func(value string) bool {
		return value == storedHash
	}).AnyTimes()
	s.manager = New(s.store, s.keys, s.notes, s.links, s.hasher)
}

func (s *ManagerTestSuite) SetupSubTest() { s.SetupTest() }

func (s *ManagerTestSuite) TearDownTest() { s.ctrl.Finish() }

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

func (s *ManagerTestSuite) stubKeyring() {
	s.keys.EXPECT().ActiveKey().Return(activeKey).AnyTimes()
	s.keys.EXPECT().KeyIDs().DoAndReturn(func() []string { return s.keyIDs }).AnyTimes()
	s.keys.EXPECT().KeyCheck(gomock.Any(), gomock.Any()).DoAndReturn(func(_, id string) (string, error) {
		return checkFor(id), nil
	}).AnyTimes()
	s.keys.EXPECT().VerifyKeyCheck(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_, id, value string) error {
			if s.substituted[id] || value != checkFor(id) {
				return errKeyMismatch
			}

			return nil
		}).AnyTimes()
}

func (s *ManagerTestSuite) fixture(value string) cipherFixture {
	s.T().Helper()

	if strings.HasPrefix(value, convertedPrefix) {
		return cipherFixture{
			versioned: true, kind: "p", key: activeKey,
			legacyErr: nil, inspectErr: nil,
		}
	}

	fixture, ok := fixtures[value]
	s.Require().True(ok, "no cipher fixture for value")

	return fixture
}

func (s *ManagerTestSuite) stubCipher(cipher *MockCipher) {
	cipher.EXPECT().Versioned(gomock.Any()).DoAndReturn(func(value string) bool {
		return s.fixture(value).versioned
	}).AnyTimes()
	cipher.EXPECT().ValidateLegacy(gomock.Any()).DoAndReturn(func(value string) error {
		return s.fixture(value).legacyErr
	}).AnyTimes()
	cipher.EXPECT().Inspect(gomock.Any(), gomock.Any()).DoAndReturn(func(value, _ string) (string, string, error) {
		fixture := s.fixture(value)
		if !fixture.versioned {
			return "", "", errMalformed
		}

		return fixture.kind, fixture.key, fixture.inspectErr
	}).AnyTimes()
	cipher.EXPECT().WrapLegacy(gomock.Any(), gomock.Any()).DoAndReturn(func(value, _ string) (string, error) {
		fixture := s.fixture(value)
		if fixture.versioned || fixture.legacyErr != nil {
			return "", errMalformed
		}

		return wrappedValue, nil
	}).AnyTimes()
}

// expectRows serves Batch from rows for the whole test.
func (s *ManagerTestSuite) expectRows(rows *[]Row) {
	s.store.EXPECT().Batch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(pageRows(rows)).AnyTimes()
}

// expectDeletes removes matching rows on Delete, reporting a CAS conflict otherwise.
func (s *ManagerTestSuite) expectDeletes(rows *[]Row, times int) {
	s.store.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, row Row) (bool, error) {
		before := len(*rows)
		*rows = slices.DeleteFunc(*rows, func(stored Row) bool { return stored == row })

		return len(*rows) < before, nil
	}).Times(times)
}

func (s *ManagerTestSuite) expectRegistry(registry *Registry) {
	s.store.EXPECT().Registry(gomock.Any()).Return(registry, nil).AnyTimes()
}

// Migrate and prune

func (s *ManagerTestSuite) TestMigrateInitializesPendingWithCorruptRaw() {
	ctx := s.T().Context()
	corrupt := newRow(resourceNote, "corrupt-id", corruptValue)
	legacy := newRow(resourceLink, "legacy-id", legacyValue)
	rows := []Row{corrupt, legacy}

	var registry *Registry

	s.expectRows(&rows)
	s.store.EXPECT().Registry(gomock.Any()).DoAndReturn(func(context.Context) (*Registry, error) {
		return registry, nil
	}).AnyTimes()
	s.store.EXPECT().Initialize(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, reg *Registry) error {
		s.Equal(Pending, reg.Phase, "malformed raw records may bind only a pending registry")
		s.Equal(checkFor(activeKey), reg.Checks[activeKey])

		registry = reg

		return nil
	})
	s.store.EXPECT().SetPhase(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, phase string) error {
		registry.Phase = phase

		return nil
	}).Times(3)
	s.store.EXPECT().Replace(gomock.Any(), legacy, wrappedValue).DoAndReturn(
		func(context.Context, Row, string) (bool, error) {
			rows[1].Value = wrappedValue

			return true, nil
		})
	s.expectDeletes(&rows, 1)

	report, err := s.manager.Migrate(ctx, false)
	s.Require().ErrorContains(err, "prune --corrupt --dry-run")
	s.Equal(1, report.CorruptRaw)
	s.Equal(1, report.Failed)
	s.Equal(1, report.Changed)
	s.Equal(1, report.Wrapped)
	s.Equal(Pending, report.State)
	s.Require().NotNil(registry)
	s.Require().Error(s.manager.PrepareServe(ctx))

	report, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().NoError(err)
	s.Equal(1, report.Pruned)
	s.Equal(Pending, report.State)
	s.Require().ErrorContains(s.manager.PrepareServe(ctx), "content migration required",
		"pruning must not finish the migration implicitly")

	report, err = s.manager.Migrate(ctx, false)
	s.Require().NoError(err)
	s.Equal(Ready, report.State)
	s.Require().NoError(s.manager.PrepareServe(ctx))
}

func (s *ManagerTestSuite) TestPruneDryRunDoesNotInitializeOrDelete() {
	ctx := s.T().Context()
	rows := []Row{newRow(resourceNote, "corrupt-id", corruptValue)}

	s.expectRegistry(nil)
	s.expectRows(&rows)

	report, err := s.manager.PruneCorrupt(ctx, true)
	s.Require().NoError(err)
	s.Equal(1, report.CorruptRaw)
	s.Equal(1, report.Failed)
	s.Zero(report.Pruned)
	s.Len(rows, 1)

	_, err = s.manager.Migrate(ctx, true)
	s.Require().Error(err)

	s.expectDeletes(&rows, 1)

	report, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().NoError(err)
	s.Equal(1, report.Pruned)
	s.Empty(rows)
}

func (s *ManagerTestSuite) TestPrunePreservesValidRecords() {
	rows := []Row{
		newRow(resourceNote, "corrupt", corruptValue),
		newRow(resourceNote, "dollar", dollarValue),
		newRow(resourceNote, "legacy", legacyValue),
		newRow(resourceNote, "wrapped", wrappedValue),
		newRow(resourceNote, "native", nativeValue),
	}
	valid := slices.Clone(rows[2:])

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)
	s.expectDeletes(&rows, 2)

	report, err := s.manager.PruneCorrupt(s.T().Context(), false)
	s.Require().NoError(err)
	s.Equal(2, report.Pruned)
	s.Equal(1, report.Raw)
	s.Equal(1, report.Wrapped)
	s.Equal(1, report.Native)
	s.Zero(report.CorruptRaw)
	s.ElementsMatch(valid, rows)
}

func (s *ManagerTestSuite) TestCorruptRawWithDollarIsPrunable() {
	ctx := s.T().Context()
	rows := []Row{newRow(resourceLink, "dollar", dollarValue), newRow(resourceNote, "native", nativeValue)}

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)

	report, err := s.manager.Status(ctx)
	s.Require().ErrorContains(err, "prune --corrupt --dry-run", "only malformed raw data remains")
	s.Equal(1, report.CorruptRaw)
	s.Equal(1, report.Failed)
	s.Equal(1, report.Native)
	s.Require().Error(s.manager.PrepareServe(ctx), "a corrupt raw row must keep serve blocked")

	s.expectDeletes(&rows, 1)

	report, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().NoError(err)
	s.Equal(1, report.Pruned)
	s.Equal([]Row{newRow(resourceNote, "native", nativeValue)}, rows)
}

func (s *ManagerTestSuite) TestInvalidEnvelopeCannotBePrunedOrBindNewRegistry() {
	for _, value := range []string{futureValue, garbageValue} {
		s.Run(value, func() {
			ctx := s.T().Context()
			rows := []Row{
				newRow(resourceNote, "corrupt-id", corruptValue),
				newRow(resourceLink, "envelope", value),
			}

			// Neither Initialize nor Delete is expected: any call fails the test.
			s.expectRegistry(nil)
			s.expectRows(&rows)

			_, err := s.manager.Migrate(ctx, false)
			s.Require().Error(err)

			report, err := s.manager.Status(ctx)
			s.Require().ErrorContains(err, "versioned envelopes failed authentication")
			s.NotContains(err.Error(), "prune --corrupt --dry-run", "the hint must not point at a blocked prune")
			s.Equal(2, report.Failed)
			s.Equal(1, report.CorruptRaw, "versioned values are never malformed raw records")

			for _, dryRun := range []bool{true, false} {
				report, err := s.manager.PruneCorrupt(ctx, dryRun)
				s.Require().Error(err)
				s.Zero(report.Pruned)
				s.Len(rows, 2)
			}
		})
	}
}

func (s *ManagerTestSuite) TestMixedDatabaseClassification() {
	rows := []Row{
		newRow(resourceNote, "legacy", legacyValue),
		newRow(resourceNote, "corrupt", corruptValue),
		newRow(resourceLink, "dollar", dollarValue),
		newRow(resourceNote, "native", nativeValue),
		newRow(resourceLink, "wrapped", wrappedValue),
		newRow(resourceLink, "future", futureValue),
	}

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)

	report, err := s.manager.Status(s.T().Context())
	s.Require().ErrorContains(err, "refuses to run until they authenticate")
	s.NotContains(err.Error(), "prune --corrupt --dry-run")
	s.Equal(1, report.Raw)
	s.Equal(2, report.CorruptRaw)
	s.Equal(3, report.Failed)
	s.Equal(1, report.Native)
	s.Equal(1, report.Wrapped)
	s.Equal(map[string]int{activeKey: 2}, report.Keys)

	report, err = s.manager.PruneCorrupt(s.T().Context(), false)
	s.Require().Error(err, "a failing envelope blocks the whole prune before any deletion")
	s.Zero(report.Pruned)
	s.Len(rows, 6)
}

func (s *ManagerTestSuite) TestWrongKeyCannotPruneOrInitialize() {
	ctx := s.T().Context()
	rows := []Row{newRow(resourceNote, "corrupt-id", corruptValue), newRow(resourceNote, "native", wrongKeyValue)}

	s.expectRegistry(nil)
	s.expectRows(&rows)

	_, err := s.manager.Migrate(ctx, false)
	s.Require().Error(err)

	_, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().Error(err)
	s.Len(rows, 2)
}

// Registry checks must still reject a substituted key even with no envelope
// references remaining to authenticate it independently.
func (s *ManagerTestSuite) TestSubstitutedKeyRejectedByRegistryCheck() {
	ctx := s.T().Context()
	rows := []Row{newRow(resourceNote, "corrupt-id", corruptValue)}
	s.substituted[activeKey] = true

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)

	_, err := s.manager.Migrate(ctx, false)
	s.Require().ErrorContains(err, "does not match")

	_, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().ErrorContains(err, "does not match")
	s.Len(rows, 1)
}

func (s *ManagerTestSuite) TestReadFailureDoesNotInitializeOrPrune() {
	ctx := s.T().Context()

	s.expectRegistry(nil)
	s.store.EXPECT().Batch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("scan failed")).AnyTimes()

	_, err := s.manager.Migrate(ctx, false)
	s.Require().ErrorContains(err, "scan failed")

	_, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().ErrorContains(err, "scan failed")
	s.Equal(1, countPrefix(err))
}

func (s *ManagerTestSuite) TestPruneConflictPreservesConcurrentReplacement() {
	rows := []Row{newRow(resourceNote, "corrupt-id", corruptValue)}

	s.expectRegistry(nil)
	s.expectRows(&rows)
	s.store.EXPECT().Delete(gomock.Any(), rows[0]).DoAndReturn(func(context.Context, Row) (bool, error) {
		rows[0].Value = nativeValue // a concurrent writer replaced the value before the CAS delete

		return false, nil
	})

	report, err := s.manager.PruneCorrupt(s.T().Context(), false)
	s.Require().NoError(err)
	s.Zero(report.Pruned)
	s.Equal(1, report.Conflicts)
	s.Equal(1, report.Native)
	s.Equal(nativeValue, rows[0].Value)
}

func (s *ManagerTestSuite) TestPruneDeleteFailureKeepsRecord() {
	rows := []Row{newRow(resourceNote, "corrupt-id", corruptValue)}

	s.expectRegistry(nil)
	s.expectRows(&rows)
	s.store.EXPECT().Delete(gomock.Any(), rows[0]).Return(false, errors.New("delete failed"))

	report, err := s.manager.PruneCorrupt(s.T().Context(), false)
	s.Require().ErrorContains(err, "delete failed")
	s.Equal(1, countPrefix(err), "joined failure and status errors carry the context once")
	s.Zero(report.Pruned)
	s.Equal(1, report.CorruptRaw)
	s.Len(rows, 1)
}

// Convert

func (s *ManagerTestSuite) TestConvertSlugsCountsUniqueFailures() {
	cases := map[string]string{
		// A valid outer envelope cannot authenticate the original legacy slug.
		"inner_legacy_failure": wrappedValue,
		// Corruption happens after the initial readyReport, before conversion.
		"structural_failure": garbageValue,
		"unversioned_value":  dollarValue,
	}

	for name, seen := range cases {
		s.Run(name, func() {
			row := newRow(resourceNote, "original-slug", wrappedValue)
			rows := []Row{row}

			s.expectRegistry(readyRegistry())
			s.expectRows(&rows)
			s.store.EXPECT().Get(gomock.Any(), resourceNote, row.ID).DoAndReturn(
				func(context.Context, string, string) (*Row, error) {
					rows[0].Value = seen
					current := rows[0]

					return &current, nil
				}).AnyTimes()
			s.store.EXPECT().Get(gomock.Any(), resourceLink, row.ID).Return(nil, nil).AnyTimes()
			s.notes.EXPECT().Decrypt(wrappedValue, row.ID, "original-slug").
				Return("", errDecryption).AnyTimes()

			for _, dryRun := range []bool{true, false} {
				rows[0].Value = wrappedValue // the next initial scan must pass

				report, err := s.manager.ConvertSlugs(s.T().Context(),
					strings.NewReader(strings.Repeat("original-slug\n", 2)), dryRun)
				s.Require().Error(err)
				s.Equal(1, report.Failed, "each row is counted once, including repeated input")
				s.Zero(report.Changed)
			}
		})
	}
}

// Register and key removal

func (s *ManagerTestSuite) TestRegisterAddsCheckOnce() {
	ctx := s.T().Context()
	registry := readyRegistry()
	rows := []Row{}
	s.keyIDs = []string{activeKey, spareKey}

	s.expectRegistry(registry)
	s.expectRows(&rows)
	s.store.EXPECT().Register(gomock.Any(), spareKey, checkFor(spareKey)).DoAndReturn(
		func(_ context.Context, id, check string) error {
			registry.Checks[id] = check

			return nil
		})

	s.Require().NoError(s.manager.Register(ctx, spareKey))
	s.Require().NoError(s.manager.Register(ctx, spareKey), "registering again is a no-op")
	s.Equal(checkFor(spareKey), registry.Checks[spareKey])
}

func (s *ManagerTestSuite) TestRegisterKeyIDReuseNeverOverwritesVerifier() {
	registry := readyRegistry()
	registry.Checks[spareKey] = checkFor(spareKey)
	rows := []Row{}
	s.keyIDs = []string{activeKey, spareKey}
	s.substituted[spareKey] = true

	// No Register call is expected: the verifier must never be replaced.
	s.expectRegistry(registry)
	s.expectRows(&rows)

	s.Require().ErrorContains(s.manager.Register(s.T().Context(), spareKey), "does not match")
	s.Equal(checkFor(spareKey), registry.Checks[spareKey])
}

func (s *ManagerTestSuite) TestRegisterRequiresInitializedRegistry() {
	s.expectRegistry(nil)

	s.Require().ErrorContains(s.manager.Register(s.T().Context(), spareKey), "initialize encryption")
}

func (s *ManagerTestSuite) TestKeyRemovalGates() {
	ctx := s.T().Context()
	registry := readyRegistry()
	registry.Checks[spareKey] = checkFor(spareKey)
	rows := []Row{}
	s.keyIDs = []string{activeKey, spareKey}

	s.expectRegistry(registry)
	s.expectRows(&rows)

	s.Require().ErrorContains(s.manager.CanRemove(ctx, activeKey), "active key")
	s.Require().NoError(s.manager.CanRemove(ctx, spareKey))

	rows = append(rows, newRow(resourceNote, "referenced", spareValue))

	s.Require().ErrorContains(s.manager.CanRemove(ctx, spareKey), "still referenced")

	registry.Phase = Pending
	rows = rows[:0]

	s.Require().ErrorContains(s.manager.CanRemove(ctx, spareKey), "content migration required")
}

// Errors

func (s *ManagerTestSuite) TestWrapErrorAddsContextOnce() {
	cause := errors.New("registry down")

	wrapped := Wrap(Wrap(cause))
	s.Require().ErrorIs(wrapped, cause)
	s.Equal("content encryption: registry down", wrapped.Error())
	s.Equal(wrapped, Wrap(wrapped))
	s.Require().NoError(Wrap(nil))

	s.store.EXPECT().Registry(gomock.Any()).Return(nil, cause).AnyTimes()

	err := Wrap(s.manager.PrepareServe(s.T().Context()))
	s.Require().ErrorIs(err, cause)
	s.Equal(1, countPrefix(err))
}
