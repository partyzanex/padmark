package cli

import (
	"fmt"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// filesOffMessage is the message of a /files response when the server has no object storage.
// The server sends no machine-readable code for this case, so the CLI compares the message
// (domain.ErrFeatureNotSupported). A 503 with a different message, for example "file storage is
// unavailable", is a temporary failure and keeps the server message.
const filesOffMessage = "feature is not enabled"

// filesOffCode is the code that a server can send when file publishing is off.
const filesOffCode = "files_disabled"

// serviceUnavailableError distinguishes an operator-disabled API from other 503 responses.
func serviceUnavailableError(response padmark.ErrorResponse) error {
	if response.Code.Or("") == "api_disabled" {
		return ErrAPIDisabled
	}

	return fmt.Errorf("%w: %s", ErrServiceUnavailable, response.Message)
}

// fileServiceUnavailableError maps a 503 response of a /files operation. It also identifies a
// server that has file publishing off.
func fileServiceUnavailableError(code, message string) error {
	switch {
	case code == "api_disabled":
		return ErrAPIDisabled
	case code == filesOffCode, message == filesOffMessage:
		return ErrFilesDisabled
	default:
		return fmt.Errorf("%w: %s", ErrServiceUnavailable, message)
	}
}
