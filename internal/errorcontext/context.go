// Package errorcontext adds operation context without changing successful nil errors.
package errorcontext

import (
	"fmt"
)

// Wrap describes the failed operation and preserves the cause for errors.Is and errors.As.
func Wrap(err error, operation string) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", operation, err)
}
