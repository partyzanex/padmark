package server

import (
	"errors"
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// The validator errors stay next to their validators, because only the flag set uses them.
var (
	errMaxFileSizeTooLarge     = errors.New("max file size must not be larger than 48 GiB (51539607552 bytes)")
	errInvalidShutdownTimeout  = errors.New("shutdown timeout must be larger than 0")
	errInvalidMaxContentLength = errors.New("max content length must not be negative; " +
		"use 0 for the --max-body-bytes value")
	errMaxContentLengthTooLarge = errors.New("max content length must not be larger than 67108864 " +
		"characters: a note holds at most 64 MiB")
	errInvalidFileIdleTimeout = errors.New("file idle timeout must not be negative; use 0 for the default")
	errInvalidUploadLimit     = errors.New("upload limit must not be negative; use 0 for the default")
)

// validateMaxFileSize keeps --max-file-size between 1 byte and maxFileSizeLimit.
func validateMaxFileSize(value int64) error {
	if value < 1 {
		return errInvalidMaxFileSize
	}

	if value > maxFileSizeLimit {
		return errMaxFileSizeTooLarge
	}

	return nil
}

// validateSweepInterval rejects a negative --files-sweep-interval; 0 turns the sweep off.
func validateSweepInterval(value time.Duration) error {
	if value < 0 {
		return errInvalidSweepInterval
	}

	return nil
}

// validateShutdownTimeout rejects a --shutdown-timeout that is not positive: a zero limit
// would skip the shutdown of each resource.
func validateShutdownTimeout(value time.Duration) error {
	if value <= 0 {
		return errInvalidShutdownTimeout
	}

	return nil
}

// validateFileIdleTimeout rejects a negative --file-idle-timeout; 0 selects the default.
func validateFileIdleTimeout(value time.Duration) error {
	if value < 0 {
		return errInvalidFileIdleTimeout
	}

	return nil
}

// validateMaxContentLength rejects a negative --max-content-length; 0 selects the
// --max-body-bytes value. It also rejects a value above domain.MaxContentBytes: each character
// has at least one byte, so the server can never accept such a note.
func validateMaxContentLength(value int) error {
	if value < 0 {
		return errInvalidMaxContentLength
	}

	if value > domain.MaxContentBytes {
		return errMaxContentLengthTooLarge
	}

	return nil
}

// validateUploadLimit rejects a negative --max-uploads or --max-uploads-per-client; 0 selects
// the default.
func validateUploadLimit(value int) error {
	if value < 0 {
		return errInvalidUploadLimit
	}

	return nil
}

// maxArgon2MemoryBudget caps --argon2-memory-budget at 64 GiB (in KiB): it rejects typos and keeps
// the value inside uint32 on every platform's int.
const maxArgon2MemoryBudget = 64 * 1024 * 1024

// boundedCost rejects negative and overflowing flag values before narrowing to uint32/uint8.
func boundedCost(maximum int) func(int) error {
	return func(value int) error {
		if value < 1 || value > maximum {
			return fmt.Errorf("%w 1 and %d", errInvalidArgon2Cost, maximum)
		}

		return nil
	}
}
