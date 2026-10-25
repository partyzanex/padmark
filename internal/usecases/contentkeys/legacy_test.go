package contentkeys

import (
	"context"
	"errors"
	"slices"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// convertedPrefix marks the mocked native envelope the notes cipher returns for a converted row.
const convertedPrefix = "pmc2$p$key$converted-"

// legacyRow is a v0.1.6 note as the schema migration tags it: plaintext slug as ID,
// plaintext content and edit code.
func legacyRow(slug, value, editCode string) Row {
	return Row{Resource: resourceNote, ID: slug, Value: value, Legacy: &LegacyNote{Slug: slug, EditCode: editCode}}
}

// expectEncrypt returns the converted envelope bound to the record ID it was encrypted for.
func (s *ManagerTestSuite) expectEncrypt(times int) {
	s.notes.EXPECT().Encrypt(gomock.Any(), gomock.Any()).DoAndReturn(func(_, recordID string) (string, error) {
		return convertedPrefix + recordID, nil
	}).Times(times)
}

// expectConvert applies the compare-and-swap to rows, checking the converted values.
func (s *ManagerTestSuite) expectConvert(rows *[]Row, hashes map[string]string, times int) {
	s.store.EXPECT().ConvertLegacy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, row Row, newID, value, editCodeHash string) (bool, error) {
			s.Equal(domain.HashSlug(row.Legacy.Slug), newID, "the new ID is the hashed original slug")
			s.Equal(convertedPrefix+newID, value, "content is encrypted for the new ID")
			s.Equal(hashes[row.Legacy.Slug], editCodeHash)

			index := slices.Index(*rows, row)
			if index < 0 {
				return false, nil
			}

			(*rows)[index] = Row{Resource: resourceNote, ID: newID, Value: value, StoredEditCode: storedHash}

			return true, nil
		}).Times(times)
}

func (s *ManagerTestSuite) TestLegacyPlaintextIsNeverMisclassified() {
	ctx := s.T().Context()
	rows := []Row{
		legacyRow("looks-versioned", garbageValue, "Abcdefgh1234"),
		legacyRow("looks-base64", legacyValue, "hunter2"),
		legacyRow("plain", corruptValue, ""),
		newRow(resourceNote, "native", nativeValue),
	}

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)

	report, err := s.manager.Status(ctx)
	s.Require().NoError(err, "tagged plaintext is neither raw ciphertext nor a failed envelope")
	s.Equal(3, report.Plaintext)
	s.Zero(report.Raw)
	s.Zero(report.Failed)
	s.Zero(report.CorruptRaw)
	s.Equal(1, report.Native)
	s.Require().ErrorContains(s.manager.PrepareServe(ctx), "content migration required")

	report, err = s.manager.PruneCorrupt(ctx, false)
	s.Require().NoError(err)
	s.Zero(report.Pruned, "prune must never delete a legacy note")
	s.Len(rows, 4)
}

func (s *ManagerTestSuite) TestMigrateConvertsLegacyPlaintextNotes() {
	ctx := s.T().Context()
	rows := []Row{
		legacyRow("generated", garbageValue, "Abcdefgh1234"),
		legacyRow("custom", legacyValue, "hunter2"),
		legacyRow("symbols", dollarValue, "abc-def_ghi!"),
		legacyRow("empty-code", corruptValue, ""),
		newRow(resourceLink, "raw-link", legacyValue),
	}
	hashes := map[string]string{
		"generated": "v1$generated", "custom": "v1$custom", "symbols": "v1$symbols", "empty-code": "",
	}

	var registry *Registry

	s.store.EXPECT().Registry(gomock.Any()).DoAndReturn(func(context.Context) (*Registry, error) {
		return registry, nil
	}).AnyTimes()
	s.store.EXPECT().Initialize(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, reg *Registry) error {
		s.Equal(Pending, reg.Phase, "plaintext rows bind a pending registry, not a failure")

		registry = reg

		return nil
	})
	s.store.EXPECT().SetPhase(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, phase string) error {
		registry.Phase = phase

		return nil
	}).Times(4) // pending + ready, for each of the two runs
	s.expectRows(&rows)
	s.hasher.EXPECT().HashUserProvided("Abcdefgh1234").Return("v1$generated", nil)
	s.hasher.EXPECT().HashUserProvided("hunter2").Return("v1$custom", nil)
	s.hasher.EXPECT().HashUserProvided("abc-def_ghi!").Return("v1$symbols", nil)
	s.expectEncrypt(4)
	s.expectConvert(&rows, hashes, 4)
	s.store.EXPECT().Replace(gomock.Any(), rows[4], wrappedValue).DoAndReturn(
		func(context.Context, Row, string) (bool, error) {
			rows[4].Value = wrappedValue

			return true, nil
		})

	report, err := s.manager.Migrate(ctx, false)
	s.Require().NoError(err)
	s.Equal(Ready, report.State)
	s.Equal(5, report.Changed)
	s.Zero(report.Conflicts)
	s.Zero(report.Plaintext)
	s.Equal(4, report.Native)
	s.Equal(1, report.Wrapped)
	s.Require().NoError(s.manager.PrepareServe(ctx))

	report, err = s.manager.Migrate(ctx, false)
	s.Require().NoError(err)
	s.Zero(report.Changed, "a converted database needs no second pass")
}

func (s *ManagerTestSuite) TestMigrateDryRunDoesNotConvertLegacy() {
	rows := []Row{legacyRow("legacy", corruptValue, "hunter2"), newRow(resourceLink, "link", nativeValue)}
	before := slices.Clone(rows)

	s.expectRegistry(nil)
	s.expectRows(&rows)

	report, err := s.manager.Migrate(s.T().Context(), true)
	s.Require().NoError(err)
	s.Equal(1, report.Plaintext)
	s.Zero(report.Changed)
	s.Equal(before, rows, "a dry run neither initializes, hashes, encrypts nor writes")
}

func (s *ManagerTestSuite) TestMigrateLegacyConflicts() {
	for name, convertErr := range map[string]error{
		"concurrent change":   nil,
		"target id exists":    ErrConvertTargetExists,
		"wrapped target race": WrapError(ErrConvertTargetExists),
	} {
		s.Run(name, func() {
			ctx := s.T().Context()
			link := newRow(resourceLink, "link", nativeValue)
			rows := []Row{legacyRow("taken", corruptValue, "hunter2"), legacyRow("free", dollarValue, ""), link}

			s.expectRegistry(readyRegistry())
			s.store.EXPECT().SetPhase(gomock.Any(), Pending).Return(nil)
			s.expectRows(&rows)
			s.hasher.EXPECT().HashUserProvided("hunter2").Return("v1$custom", nil)
			s.expectEncrypt(2)
			s.store.EXPECT().ConvertLegacy(gomock.Any(), rows[0], gomock.Any(), gomock.Any(), gomock.Any()).
				Return(false, convertErr)
			s.expectConvert(&rows, map[string]string{"free": ""}, 1)

			report, err := s.manager.Migrate(ctx, false)
			s.Require().ErrorContains(err, "migration incomplete")
			s.Equal(1, report.Conflicts)
			s.Equal(1, report.Changed, "the walk continues past a conflicting row")
			s.Equal(1, report.Plaintext, "the conflicting legacy note is kept, never dropped")
			s.Equal(link, rows[2], "links are never touched by the conversion")
			s.Require().ErrorContains(s.manager.PrepareServe(ctx), "content migration required")
		})
	}
}

func (s *ManagerTestSuite) TestMigrateLegacyStorageFailureAborts() {
	ctx := s.T().Context()
	rows := []Row{legacyRow("legacy", corruptValue, "")}
	cause := errors.New("disk I/O error")

	s.expectRegistry(readyRegistry())
	s.store.EXPECT().SetPhase(gomock.Any(), Pending).Return(nil)
	s.expectRows(&rows)
	s.expectEncrypt(1)
	s.store.EXPECT().ConvertLegacy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(false, cause)

	report, err := s.manager.Migrate(ctx, false)
	s.Require().ErrorIs(err, cause)
	s.Equal(1, countPrefix(err))
	s.Equal(1, report.Plaintext)
	s.Zero(report.Conflicts)
}
