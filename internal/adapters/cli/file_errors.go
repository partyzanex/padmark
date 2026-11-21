package cli

import (
	"errors"
	"fmt"
	"net/http"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// fileTransferError maps an error of UploadFile or DownloadFile to a CLI error. A response with
// an error status comes as *padmark.APIError. Other errors (transport, local I/O) keep their
// type, so errors.As still finds them. conflict is the error for 409, which has a different
// meaning for upload (slug conflict) and download (burn read refused).
func fileTransferError(err, conflict error) error {
	apiErr, ok := errors.AsType[*padmark.APIError](err)
	if !ok {
		return err
	}

	mapped := fileClientError(apiErr, conflict)
	if mapped != nil {
		return mapped
	}

	switch {
	case apiErr.StatusCode == http.StatusServiceUnavailable:
		return fileServiceUnavailableError(apiErr.Code, apiErr.Message)
	case apiErr.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %s", ErrServer, apiErr.Message)
	default:
		return fmt.Errorf("%w: status %d: %s", ErrUnexpectedResponse, apiErr.StatusCode, apiErr.Message)
	}
}

// fileClientError maps the 4xx statuses of a file transfer. It returns nil for other statuses,
// so the caller handles those.
func fileClientError(apiErr *padmark.APIError, conflict error) error {
	switch apiErr.StatusCode {
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrBadRequest, apiErr.Message)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, apiErr.Message)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrForbidden, apiErr.Message)
	case http.StatusNotFound:
		return ErrFileNotFound
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", conflict, apiErr.Message)
	case http.StatusGone:
		return ErrFileGone
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrFileTooLarge, apiErr.Message)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrValidation, apiErr.Message)
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return nil
	}
}
