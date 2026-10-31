package contentkeys

import (
	"context"
	"errors"
	"fmt"

	"github.com/partyzanex/padmark/internal/domain"
)

// convertLegacy turns one tagged v0.1.6 note into a current row in a single compare-and-swap:
// the ID becomes HashSlug(slug), the plaintext content a native envelope bound to that new ID,
// and the plaintext edit code a hash. A concurrent change or an already taken target ID is a
// conflict: the legacy row is kept for the final check, never overwritten or dropped.
func (m *Manager) convertLegacy(ctx context.Context, row *Row, report *Report) error {
	newID := domain.HashSlug(row.Legacy.Slug)

	value, err := m.ciphers[row.Resource].Encrypt(row.Value, newID)
	if err != nil {
		return errEncryptLegacyNote
	}

	hash, err := m.hashLegacyEditCode(row.Legacy.EditCode)
	if err != nil {
		return Wrap(err)
	}

	converted, err := m.store.ConvertLegacy(ctx, *row, newID, value, hash)
	if errors.Is(err, ErrConvertTargetExists) {
		report.Conflicts++

		return nil
	}

	if err != nil {
		return Wrap(err)
	}

	if converted {
		report.Changed++
	} else {
		report.Conflicts++
	}

	return nil
}

// hashLegacyEditCode keeps an empty code empty (it never verifies). Every nonempty legacy code
// takes the memory-hard hash: v0.1.6 stored no provenance, and a caller-chosen weak code can
// have exactly the same length and alphabet as a server-generated one.
func (m *Manager) hashLegacyEditCode(code string) (string, error) {
	if code == "" {
		return "", nil
	}

	hashed, err := m.hasher.HashUserProvided(code)
	if err != nil {
		return "", fmt.Errorf("hash legacy edit code: %w", err)
	}

	return hashed, nil
}

// MarkLegacy tags one untagged note as v0.1.6 plaintext so the next Migrate converts it in
// place (slug = id). It is the explicit fix for a v0.1.6 note that the schema migrations could
// not recognise: a custom slug of 64 hex characters with an edit code of the stored-hash shape,
// which Status reports as corrupt raw data, or an untagged_legacy note. A note with an envelope
// or with structurally valid legacy ciphertext and a hash-shaped edit code is refused: tagging it
// would encrypt ciphertext as if it were plaintext. Stop all writers before calling it.
func (m *Manager) MarkLegacy(ctx context.Context, id string) (Report, error) {
	row, err := m.store.Get(ctx, resourceNote, id)
	if err != nil {
		return newReport("unknown"), Wrap(err)
	}

	if row == nil {
		return newReport("unknown"), Wrap(ErrNoteNotFound)
	}

	if row.Legacy == nil {
		markErr := m.markRowLegacy(ctx, row)
		if markErr != nil {
			return newReport("unknown"), Wrap(markErr)
		}
	}

	report, err := m.Status(ctx)
	if err != nil && !errors.Is(err, errInvalidRecords) {
		return report, Wrap(err)
	}

	// Other records may still need attention; this report shows them, but the mark succeeded.
	return report, nil
}

func (m *Manager) markRowLegacy(ctx context.Context, row *Row) error {
	cipher := m.ciphers[resourceNote]
	if cipher.Versioned(row.Value) {
		return errNoteAlreadyEnveloped
	}

	if cipher.ValidateLegacy(row.Value) == nil && m.hasher.IsStoredHash(row.StoredEditCode) {
		return errNoteIsLegacyCiphertext
	}

	marked, err := m.store.MarkLegacy(ctx, *row)
	if err != nil {
		return Wrap(err)
	}

	if !marked {
		return errNoteChangedConcurrently
	}

	return nil
}
