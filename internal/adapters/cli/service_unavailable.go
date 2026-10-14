package cli

import (
	"errors"
	"fmt"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// serviceUnavailableError distinguishes an operator-disabled API from other 503 responses.
func serviceUnavailableError(response padmark.ErrorResponse) error {
	if response.Code.Or("") == "api_disabled" {
		return errors.New("API is disabled on this server (api_disabled); ask the operator to enable it")
	}

	return fmt.Errorf("service unavailable: %s", response.Message)
}
