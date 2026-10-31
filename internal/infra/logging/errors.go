package logging

import (
	"errors"
)

// ErrInvalidLevel reports a log level other than debug, info, warn or error.
var ErrInvalidLevel = errors.New("invalid log level")
