package contentkeys

import "fmt"

// encryptionError adds context while preserving successful nil results and error identity.
func encryptionError(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("content encryption: %w", err)
}
