package contentkeys

import (
	"context"
	"slices"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// newFileRow builds a file record: its value is the sealed metadata.
func newFileRow(slug, value string) Row {
	return Row{
		Resource: resourceFile, ID: domain.HashSlug(slug), Value: value,
		Legacy: nil, StoredEditCode: "",
	}
}

// TestCorruptFileIsNeverPrunedOrWrapped: unversioned sealed metadata of a file is damage (R-27).
// Status counts it as failed, Migrate does not wrap it as legacy ciphertext, prune refuses to
// delete the record, and the dry run names the record for manual work.
func (s *ManagerTestSuite) TestCorruptFileIsNeverPrunedOrWrapped() {
	ctx := s.T().Context()
	// legacyValue is structurally valid legacy ciphertext: for a note or a link Migrate wraps it.
	looksLegacy := newFileRow("looks-legacy", legacyValue)
	corrupt := newFileRow("corrupt", corruptValue)
	note := newRow(resourceNote, "corrupt-note", corruptValue)
	rows := []Row{looksLegacy, corrupt, note}

	s.expectRegistry(readyRegistry())
	s.expectRows(&rows)

	report, err := s.manager.Status(ctx)
	s.Require().ErrorContains(err, "reason corrupt_file")
	s.NotContains(err.Error(), "Then run encryption prune --corrupt", "prune cannot resolve a file record")
	s.Equal(2, report.CorruptFile)
	s.Equal(1, report.CorruptRaw)
	s.Equal(3, report.Failed)
	s.Zero(report.Raw, "an unversioned file value is never raw legacy ciphertext")

	// No Replace expectation: the strict mock fails the test if Migrate wraps a file value.
	s.store.EXPECT().SetPhase(gomock.Any(), Pending).Return(nil)

	report, err = s.manager.Migrate(ctx)
	s.Require().Error(err)
	s.Zero(report.Changed)
	s.Zero(report.Wrapped)
	s.Equal(2, report.CorruptFile)

	// No Delete expectation: the strict mock fails the test if prune deletes anything.
	report, err = s.manager.PruneCorrupt(ctx)
	s.Require().Error(err, "a damaged file record must block the whole prune run")
	s.Zero(report.Pruned)
	s.Equal([]Row{looksLegacy, corrupt, note}, rows)

	report, err = s.manager.PreviewPrune(ctx)
	s.Require().Error(err)
	s.ElementsMatch([]Candidate{
		{Resource: resourceFile, ID: looksLegacy.ID, Reason: reasonCorruptFile},
		{Resource: resourceFile, ID: corrupt.ID, Reason: reasonCorruptFile},
		{Resource: resourceNote, ID: note.ID, Reason: reasonCorruptRaw},
	}, report.Candidates)
}

// TestRewrapRotatesFileMetadata: key rotation rewraps the sealed metadata of files together with
// notes and links, and a file that still uses the old key stops the removal of that key (R-59).
func (s *ManagerTestSuite) TestRewrapRotatesFileMetadata() {
	ctx := s.T().Context()
	registry := readyRegistry()
	registry.Checks[spareKey] = checkFor(spareKey)
	s.keyIDs = []string{activeKey, spareKey}
	file := newFileRow("file", spareValue)
	note := newRow(resourceNote, "note", spareValue)
	link := newRow(resourceLink, "link", spareValue)
	rows := make([]Row, 0, 4)
	rows = append(rows, newFileRow("other-file", nativeValue), file)

	s.expectRegistry(registry)
	s.expectRows(&rows)

	s.Require().ErrorContains(s.manager.CanRemove(ctx, spareKey), "still referenced",
		"a file sealed with the old key must keep that key")

	rows = append(rows, note, link)

	for _, rotated := range []struct {
		cipher *MockCipher
		row    Row
	}{{s.notes, note}, {s.links, link}, {s.files, file}} {
		rotated.cipher.EXPECT().Rewrap(spareValue, rotated.row.ID).Return(nativeValue, nil)
	}

	s.files.EXPECT().Rewrap(nativeValue, rows[0].ID).Return(nativeValue, nil)
	s.store.EXPECT().Replace(gomock.Any(), gomock.Any(), nativeValue).DoAndReturn(
		func(_ context.Context, row Row, value string) (bool, error) {
			index := slices.Index(rows, row)
			if index < 0 {
				return false, nil
			}

			rows[index].Value = value

			return true, nil
		}).Times(3)

	report, err := s.manager.Rewrap(ctx)
	s.Require().NoError(err)
	s.Equal(3, report.Changed)
	s.Zero(report.Conflicts)
	s.Equal(map[string]int{activeKey: 4}, report.Keys)
	s.Require().NoError(s.manager.CanRemove(ctx, spareKey))
}
