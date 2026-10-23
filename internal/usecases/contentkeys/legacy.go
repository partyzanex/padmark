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
		return errors.New("could not encrypt converted legacy note")
	}

	hash, err := m.hashLegacyEditCode(row.Legacy.EditCode)
	if err != nil {
		return WrapError(err)
	}

	converted, err := m.store.ConvertLegacy(ctx, *row, newID, value, hash)
	if errors.Is(err, ErrConvertTargetExists) {
		report.Conflicts++

		return nil
	}

	if err != nil {
		return WrapError(err)
	}

	if converted {
		report.Changed++
	} else {
		report.Conflicts++
	}

	return nil
}

// hashLegacyEditCode keeps an empty code empty (it never verifies). A code shaped like a
// server-generated one takes the fast hash, like a newly generated code; anything else was
// chosen by the caller (v0.1.6 accepted any string) and takes the memory-hard hash.
func (m *Manager) hashLegacyEditCode(code string) (string, error) {
	if code == "" {
		return "", nil
	}

	hash := m.hasher.HashUserProvided
	if domain.HasGeneratedEditCodeShape(code) {
		hash = m.hasher.Hash
	}

	hashed, err := hash(code)
	if err != nil {
		return "", fmt.Errorf("hash legacy edit code: %w", err)
	}

	return hashed, nil
}
