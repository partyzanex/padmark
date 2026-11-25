package clientauth

import (
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"strings"
)

// FlagAllowInsecureHTTP is the flag of both binaries that permits cleartext credentials to a
// non-loopback host. The refusal message names it.
const FlagAllowInsecureHTTP = "allow-insecure-http"

// EnvAllowInsecureHTTP is the environment source of FlagAllowInsecureHTTP.
const EnvAllowInsecureHTTP = "PADMARK_ALLOW_INSECURE_HTTP"

// InsecureHTTP is the value of --allow-insecure-http. The named type keeps the setting out of
// bare bool parameters.
type InsecureHTTP bool

// InsecureHTTPDenied refuses cleartext transport to a non-loopback host.
const InsecureHTTPDenied InsecureHTTP = false

// CheckInsecure guards a credential against cleartext transport. HTTPS → nil. Plain HTTP to a
// loopback host only writes a warning to errOut, so local development keeps working. Plain HTTP
// to any other host returns ErrInsecureTransport, unless insecure permits it; then the function
// writes the warning and returns nil. A warning that cannot be written is an error, so no
// credential goes in cleartext without a notice. credential names the secret in the messages.
func CheckInsecure(errOut io.Writer, serverURL, credential string, insecure InsecureHTTP) error {
	parsed, err := url.Parse(serverURL)
	if err == nil && parsed.Scheme == "https" {
		return nil
	}

	if insecure == InsecureHTTPDenied && !isLoopbackHost(serverURL) {
		return fmt.Errorf("%w: %s to %s; use an https:// URL or pass --%s",
			ErrInsecureTransport, credential, serverURL, FlagAllowInsecureHTTP)
	}

	// The warning is the only notice that the credential travels in cleartext. When the user
	// cannot see it, the credential is not sent.
	_, err = fmt.Fprintf(errOut,
		"warning: %s will be sent in cleartext over a non-HTTPS URL (%s); use an https:// server URL\n",
		credential, serverURL)
	if err != nil {
		return fmt.Errorf("write cleartext warning: %w", err)
	}

	return nil
}

// isLoopbackHost reports whether the URL's host is localhost or a loopback IP (127.0.0.0/8, ::1).
// An unparsable URL or empty host is not loopback, so the check fails closed.
func isLoopbackHost(serverURL string) bool {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return false
	}

	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}

	addr, err := netip.ParseAddr(host)

	return err == nil && addr.IsLoopback()
}
