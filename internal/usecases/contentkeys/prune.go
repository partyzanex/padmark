package contentkeys

import (
	"context"
	"errors"
)

const (
	reasonCorruptRaw     = "corrupt_raw"
	reasonUntaggedLegacy = "untagged_legacy"
)

// PruneCorrupt permanently deletes only malformed raw records: unversioned values
// (no "pmc<digits>$" version tag, see Cipher.Versioned) that are not structurally valid
// legacy ciphertext, such as plaintext rows from before encryption, including ones that
// contain "$". Versioned values are never candidates, even when they fail authentication
// or name an unknown version; any such failure blocks the whole run before deletion.
// Valid legacy ciphertext, v0.1.6 plaintext notes tagged for conversion, and untagged v0.1.6
// notes (see untaggedLegacy) are never candidates either; an untagged v0.1.6 note also blocks
// the whole run until MarkLegacy tags it. A dry run lists every candidate ID with its reason.
// Stop all writers and back up the DB before calling with dryRun=false. It neither initializes
// the registry nor marks a pending migration ready.
func (m *Manager) PruneCorrupt(ctx context.Context, dryRun bool) (Report, error) {
	report, err := m.Status(ctx)
	if dryRun {
		if err != nil && !errors.Is(err, errInvalidRecords) {
			return report, WrapError(err)
		}

		listErr := m.listCandidates(ctx, &report)
		if listErr != nil {
			return report, WrapError(listErr)
		}

		// Malformed raw data alone is what a prune run resolves, so the preview succeeds; untagged
		// v0.1.6 notes or failing envelopes keep the error, with the candidates listed anyway.
		if onlyCorruptRaw(&report, err) {
			return report, nil
		}

		return report, WrapError(err)
	}

	if err != nil && !onlyCorruptRaw(&report, err) {
		return report, WrapError(err)
	}

	err = m.walk(ctx, func(row Row) error {
		if m.pruneReason(&row) != reasonCorruptRaw {
			return nil
		}

		deleted, deleteErr := m.store.Delete(ctx, row)
		if deleteErr != nil {
			return WrapError(deleteErr)
		}

		if deleted {
			report.Pruned++
		} else {
			report.Conflicts++
		}

		return nil
	})
	if err != nil {
		return m.failureReport(ctx, &report, err)
	}

	final, err := m.Status(ctx)
	final.Pruned, final.Conflicts = report.Pruned, report.Conflicts

	return final, WrapError(err)
}

// pruneReason classifies a row for pruning: reasonCorruptRaw for malformed raw data that prune
// deletes, reasonUntaggedLegacy for an untagged v0.1.6 note that it never deletes, or "" for
// every other row (tagged, versioned, or structurally valid legacy ciphertext).
func (m *Manager) pruneReason(row *Row) string {
	cipher := m.ciphers[row.Resource]

	switch {
	case row.Legacy != nil || cipher.Versioned(row.Value):
		return ""
	case m.untaggedLegacy(row):
		return reasonUntaggedLegacy
	case cipher.ValidateLegacy(row.Value) != nil:
		return reasonCorruptRaw
	default:
		return ""
	}
}

// listCandidates adds every record a prune run would delete or refuses to delete to the report.
func (m *Manager) listCandidates(ctx context.Context, report *Report) error {
	return m.walk(ctx, func(row Row) error {
		reason := m.pruneReason(&row)
		if reason != "" {
			report.Candidates = append(report.Candidates, Candidate{Resource: row.Resource, ID: row.ID, Reason: reason})
		}

		return nil
	})
}
