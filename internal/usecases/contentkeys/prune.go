package contentkeys

import (
	"context"
)

// PruneCorrupt permanently deletes only malformed raw records: unversioned values
// (no "pmc<digits>$" version tag, see Cipher.Versioned) that are not structurally valid
// legacy ciphertext, such as plaintext rows from before encryption, including ones that
// contain "$". Versioned values are never candidates, even when they fail authentication
// or name an unknown version; any such failure blocks the whole run before deletion.
// Valid legacy ciphertext is never a candidate either. Stop all writers and back up the
// DB before calling with dryRun=false. It neither initializes the registry nor marks a
// pending migration ready.
func (m *Manager) PruneCorrupt(ctx context.Context, dryRun bool) (Report, error) {
	report, err := m.Status(ctx)
	if err != nil && !onlyCorruptRaw(&report, err) {
		return report, WrapError(err)
	}

	if dryRun {
		return report, nil
	}

	err = m.walk(ctx, func(row Row) error {
		cipher := m.ciphers[row.Resource]
		if cipher.Versioned(row.Value) || cipher.ValidateLegacy(row.Value) == nil {
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
