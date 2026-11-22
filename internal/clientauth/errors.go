package clientauth

import (
	"errors"
)

var (
	errEmptyTrustedOrigin = errors.New("trusted origin must not be empty")
	errDefaultTransport   = errors.New("http.DefaultTransport is not an *http.Transport")
)

// Errors of the client rules. Callers match them with errors.Is.
var (
	// ErrInvalidServerURL reports a server URL without a scheme or a host.
	ErrInvalidServerURL = errors.New("has no scheme or host")
	// ErrCrossOriginRedirect reports a redirect to an origin that is not the server origin.
	ErrCrossOriginRedirect = errors.New("refusing a redirect to a different server origin")
	// ErrTooManyRedirects reports a redirect chain longer than the net/http default limit.
	ErrTooManyRedirects = errors.New("stopped after too many redirects")
	// ErrUntrustedServer reports a pmk_ envelope origin that the user did not trust yet.
	ErrUntrustedServer = errors.New("token pins server")
	// ErrInsecureTransport reports a credential that would go in cleartext to a remote host.
	ErrInsecureTransport = errors.New("refusing to send credentials over non-HTTPS to a non-loopback host")
	// ErrNoTrustTarget reports a trust request without an envelope token and without a server
	// flag: trusting the built-in default would record a decision that the user did not make.
	ErrNoTrustTarget = errors.New("nothing to trust: the token is not a pmk_ envelope")
)
