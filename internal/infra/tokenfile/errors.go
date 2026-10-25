package tokenfile

import (
	"errors"
)

var (
	errEmptyTrustedOrigin = errors.New("trusted origin must not be empty")
)

var (
	ErrInvalidServerURL = errors.New("has no scheme or host")
)
