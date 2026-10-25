package contentkeys

import (
	"errors"
)

var (
	errCiphertextConflict          = errors.New("ciphertext compare-and-swap failed")
	errCorruptRawConflict          = errors.New("corrupt raw compare-and-delete failed")
	errInvalidResource             = errors.New("invalid encryption resource")
	errLegacyMarkConflict          = errors.New("legacy note mark compare-and-swap failed")
	errLegacyNoteConflict          = errors.New("legacy note compare-and-swap failed")
	errLegacyTagRequired           = errors.New("only tagged legacy notes can be converted")
	errReadCiphertextUpdateCount   = errors.New("could not read ciphertext update count")
	errReadCorruptRawDeletionCount = errors.New("could not read corrupt raw deletion count")
	errReadLegacyConversionCount   = errors.New("could not read legacy note conversion count")
	errReadLegacyMarkCount         = errors.New("could not read legacy note mark count")
	errRegistryAbsent              = errors.New("encryption registry is absent")
	errUntaggedNoteRequired        = errors.New("only untagged notes can be marked as legacy")
)
