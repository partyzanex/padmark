package tokenfile

import (
	"errors"
)

var errEmptyTrustedOrigin = errors.New("trusted origin must not be empty")

// ErrInvalidServerURL reports a server URL without a scheme or a host.
var ErrInvalidServerURL = errors.New("has no scheme or host")
