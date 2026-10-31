package mcp

import (
	"fmt"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// serviceUnavailableError distinguishes an operator-disabled API from other 503 responses.
func serviceUnavailableError(response padmark.ErrorResponse) error {
	if response.Code.Or("") == "api_disabled" {
		return ErrAPIDisabled
	}

	return fmt.Errorf("%w: %s", ErrServiceUnavailable, response.Message)
}
