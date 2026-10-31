package contentkeys

import (
	"go.uber.org/mock/gomock"
)

// hexSlug is a v0.1.6 custom slug of 64 lowercase hex characters: it looks like a hashed ID, so
// only the edit code can show that the row is still v0.1.6 plaintext (review R1).
const hexSlug = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// untaggedRow is a v0.1.6 note that no schema migration tagged: its edit code is plaintext.
func untaggedRow(value, editCode string) Row {
	return Row{
		Resource: resourceNote, ID: hexSlug, Value: value, StoredEditCode: editCode,
		Legacy: nil,
	}
}

// TestUntaggedLegacyBlocksAndIsNeverPruned: an untagged v0.1.6 note whose edit code only starts
// like a hash is reported on its own, advises mark-legacy, blocks a prune run and is never deleted.
func (s *ManagerTestSuite) TestUntaggedLegacyBlocksAndIsNeverPruned() {
	ctx := s.T().Context()
	untagged := untaggedRow(corruptValue, "v1$old-user-code")
	corrupt := newRow(resourceNote, "corrupt-id", corruptValue)
	rows := []Row{untagged, corrupt}

	s.expectRegistry(nil)
	s.expectRows(&rows)

	report, err := s.manager.Status(ctx)
	s.Require().ErrorContains(err, "mark-legacy --id")
	s.Equal(1, report.UntaggedLegacy)
	s.Equal(1, report.CorruptRaw)
	s.Equal(2, report.Failed)

	// No Delete expectation: the strict mock fails the test if prune deletes anything.
	report, err = s.manager.PruneCorrupt(ctx)
	s.Require().Error(err, "an untagged v0.1.6 note must block the whole prune run")
	s.Zero(report.Pruned)
	s.Len(rows, 2)

	report, err = s.manager.PreviewPrune(ctx)
	s.Require().Error(err)
	s.ElementsMatch([]Candidate{
		{Resource: resourceNote, ID: untagged.ID, Reason: reasonUntaggedLegacy},
		{Resource: resourceNote, ID: corrupt.ID, Reason: reasonCorruptRaw},
	}, report.Candidates)
}

// TestPruneDryRunListsCorruptCandidates: with malformed raw data only, the preview succeeds and
// names the record a prune run would delete.
func (s *ManagerTestSuite) TestPruneDryRunListsCorruptCandidates() {
	ctx := s.T().Context()
	corrupt := newRow(resourceNote, "corrupt-id", corruptValue)
	rows := []Row{corrupt, newRow(resourceNote, "native", nativeValue)}

	s.expectRegistry(nil)
	s.expectRows(&rows)

	report, err := s.manager.PreviewPrune(ctx)
	s.Require().NoError(err)
	s.Equal([]Candidate{{Resource: resourceNote, ID: corrupt.ID, Reason: reasonCorruptRaw}}, report.Candidates)
}

// TestMigrateNeverWrapsUntaggedLegacy: base64-looking v0.1.6 plaintext with a plaintext edit code
// is not legacy ciphertext, so Migrate must not wrap it even on an initialized registry.
func (s *ManagerTestSuite) TestMigrateNeverWrapsUntaggedLegacy() {
	ctx := s.T().Context()
	rows := []Row{untaggedRow(legacyValue, "s1$old-user-code")}

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)
	s.store.EXPECT().SetPhase(gomock.Any(), Pending).Return(nil)

	// No Replace expectation: wrapping the row would be an unexpected call.
	report, err := s.manager.Migrate(ctx)
	s.Require().ErrorContains(err, "mark-legacy --id")
	s.Equal(1, report.UntaggedLegacy)
	s.Zero(report.Raw)
	s.Equal(legacyValue, rows[0].Value)
}

// taggedRow is row after MarkLegacy: the schema-migration tag and the plaintext edit code.
func taggedRow(row Row) Row {
	row.Legacy = &LegacyNote{Slug: row.ID, EditCode: row.StoredEditCode}
	row.StoredEditCode = ""

	return row
}

// TestMarkLegacy_Tags: both untagged v0.1.6 shapes are tagged and then count as plaintext.
func (s *ManagerTestSuite) TestMarkLegacy_Tags() {
	marks := []struct {
		name string
		row  Row
	}{
		// The residual R1 case: a caller-chosen code of the exact stored-hash shape.
		{"corrupt raw with a hash-shaped code", untaggedRow(corruptValue, storedHash)},
		{"base64-looking plaintext with a plaintext code", untaggedRow(legacyValue, "v1$old-user-code")},
	}

	for _, mark := range marks {
		s.Run(mark.name, func() {
			rows := []Row{taggedRow(mark.row)}

			s.store.EXPECT().Get(gomock.Any(), resourceNote, hexSlug).Return(&mark.row, nil)
			s.store.EXPECT().MarkLegacy(gomock.Any(), mark.row).Return(true, nil)
			s.expectRegistry(nil)
			s.expectRows(&rows)

			report, err := s.manager.MarkLegacy(s.T().Context(), hexSlug)
			s.Require().NoError(err)
			s.Equal(1, report.Plaintext)
			s.Zero(report.Failed)
		})
	}
}

func (s *ManagerTestSuite) TestMarkLegacy_AlreadyTaggedIsANoOp() {
	row := taggedRow(untaggedRow(corruptValue, "hunter2"))
	rows := []Row{row}

	s.store.EXPECT().Get(gomock.Any(), resourceNote, hexSlug).Return(&row, nil)
	s.expectRegistry(nil)
	s.expectRows(&rows)

	report, err := s.manager.MarkLegacy(s.T().Context(), hexSlug)
	s.Require().NoError(err)
	s.Equal(1, report.Plaintext)
}

// TestMarkLegacy_Refusals: a missing note, an envelope and real legacy ciphertext are never tagged.
func (s *ManagerTestSuite) TestMarkLegacy_Refusals() {
	refusals := []struct {
		name string
		row  *Row
		want string
	}{
		{"missing note", nil, "note not found"},
		{
			"envelope", &Row{
				Resource: resourceNote, ID: hexSlug, Value: nativeValue, StoredEditCode: storedHash,
				Legacy: nil,
			},
			"encryption envelope",
		},
		{"legacy ciphertext with a hashed code", &Row{
			Resource: resourceNote, ID: hexSlug, Value: legacyValue, StoredEditCode: storedHash,

			Legacy: nil,
		}, "legacy ciphertext"},
	}

	for _, refusal := range refusals {
		s.Run(refusal.name, func() {
			// No MarkLegacy expectation: the strict mock fails the test if the row is tagged.
			s.store.EXPECT().Get(gomock.Any(), resourceNote, hexSlug).Return(refusal.row, nil)

			_, err := s.manager.MarkLegacy(s.T().Context(), hexSlug)
			s.Require().ErrorContains(err, refusal.want)
		})
	}
}

func (s *ManagerTestSuite) TestMarkLegacy_ConcurrentChange() {
	row := untaggedRow(corruptValue, storedHash)

	s.store.EXPECT().Get(gomock.Any(), resourceNote, hexSlug).Return(&row, nil)
	s.store.EXPECT().MarkLegacy(gomock.Any(), row).Return(false, nil)

	_, err := s.manager.MarkLegacy(s.T().Context(), hexSlug)
	s.Require().ErrorContains(err, "changed concurrently")
}
