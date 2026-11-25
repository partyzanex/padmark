package clientauth

import (
	"fmt"
	"io"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// bearerCredential names the bearer token in the plain-HTTP messages.
const bearerCredential = "bearer token"

// Settings are the client settings of one binary run. Each adapter reads them from its flags.
type Settings struct {
	// ErrOut receives the warnings: a cleartext credential and a token file with a loose mode.
	ErrOut io.Writer
	// Token is the --token or PADMARK_TOKEN value. An empty value reads the token file.
	Token string
	// ServerURL is the value of the server flag. It is the flag default when ServerURLSet is false.
	ServerURL string
	// Program is the binary name in the messages, for example "padmark-cli".
	Program string
	// ServerFlag is the name of the server flag in the messages, for example "url".
	ServerFlag string
	// HeaderTimeout limits the wait for the response headers. Zero turns the limit off.
	HeaderTimeout time.Duration
	// ServerURLSet tells an explicit server flag or PADMARK_URL from the flag default.
	ServerURLSet bool
	// InsecureHTTP is the --allow-insecure-http setting.
	InsecureHTTP InsecureHTTP
}

// Server is the server and the credential of one binary run.
type Server struct {
	// URL is the base URL that the requests go to.
	URL string
	// bearer is the key of the Authorization header. Empty sends no header.
	bearer string
	// embeddedURL is the server URL of a pmk_ envelope token, or empty for a bare key.
	embeddedURL string
}

// HasToken reports whether the requests carry a bearer token.
func (srv Server) HasToken() bool {
	return srv.bearer != ""
}

// ResolveServer reads the token once and selects the server URL. An explicit server flag wins,
// then the URL in a pmk_ envelope token, then the flag default. It does not check trust: use it
// only to show or compare the URL. Requests must use TrustedServer or NewClient. The only error
// is a token file warning that cannot be written (ReadToken).
func ResolveServer(settings *Settings) (Server, error) {
	token := settings.Token
	if token == "" {
		fileToken, err := ReadToken(settings.ErrOut)
		if err != nil {
			return Server{URL: "", bearer: "", embeddedURL: ""}, fmt.Errorf("read token file: %w", err)
		}

		token = fileToken
	}

	bearer, embeddedURL := SplitToken(token)

	serverURL := settings.ServerURL
	if embeddedURL != "" && !settings.ServerURLSet {
		serverURL = embeddedURL
	}

	return Server{URL: serverURL, bearer: bearer, embeddedURL: embeddedURL}, nil
}

// TrustedServer is the gate of each request before network I/O. It resolves the server and
// enforces trust-on-first-use for the URL of a pmk_ envelope token. It fails closed: on a trust
// error it returns no server, so no request can go to it.
func TrustedServer(settings *Settings) (Server, error) {
	server, err := ResolveServer(settings)
	if err != nil {
		return server, err
	}

	err = checkTrusted(settings, server)
	if err != nil {
		return Server{URL: "", bearer: "", embeddedURL: ""}, fmt.Errorf("validate trusted server: %w", err)
	}

	return server, nil
}

// NewClient returns the one API client of a binary run, and the server that it calls. It
// checks trust, refuses a bearer token over plain HTTP to a remote host and builds the HTTP
// client with the redirect guard (NewHTTPClient). It opens no connection.
func NewClient(settings *Settings) (*padmark.Client, Server, error) {
	server, err := TrustedServer(settings)
	if err != nil {
		return nil, server, err
	}

	if server.HasToken() {
		insecureErr := CheckInsecure(settings.ErrOut, server.URL, bearerCredential, settings.InsecureHTTP)
		if insecureErr != nil {
			return nil, server, fmt.Errorf("validate token transport: %w", insecureErr)
		}
	}

	httpClient, err := NewHTTPClient(server.URL, server.bearer, settings.HeaderTimeout)
	if err != nil {
		return nil, server, fmt.Errorf("create http client: %w", err)
	}

	apiClient, err := padmark.NewClient(server.URL, padmark.WithClient(httpClient))
	if err != nil {
		return nil, server, fmt.Errorf("create client: %w", err)
	}

	return apiClient, server, nil
}

// SplitToken separates a configured token value into the bearer key and the server URL in it.
// It is the client counterpart of domain.EncodeAPITokenEnvelope: an envelope token gives its key
// and URL, and a bare key gives itself and an empty URL.
func SplitToken(raw string) (bearer, serverURL string) {
	if embeddedURL, key, ok := domain.DecodeAPITokenEnvelope(raw); ok {
		return key, embeddedURL
	}

	return raw, ""
}

// checkTrusted enforces trust-on-first-use for the URL of a pmk_ envelope token. A third party
// can give a token with any URL, and the requests would carry the content, the Authorization
// header and the edit codes to that URL. Only an envelope that selects the server needs the
// check: an explicit server flag is its own confirmation, and a bare key selects nothing.
func checkTrusted(settings *Settings, server Server) error {
	if server.embeddedURL == "" || settings.ServerURLSet {
		return nil
	}

	origin, err := Origin(server.URL)
	if err != nil {
		return fmt.Errorf("token pins an invalid server URL %q: %w", server.URL, err)
	}

	if IsKnownOrigin(origin) {
		return nil
	}

	return fmt.Errorf("%w %s, which is not trusted yet; run `%s trust` to record it, or choose "+
		"the server explicitly with --%s/PADMARK_URL", ErrUntrustedServer, origin, settings.Program,
		settings.ServerFlag)
}

// Trust records the server as trusted for pmk_ envelope tokens and returns its origin and the
// path of the record. It selects the server as the requests do: an explicit server flag wins,
// then the URL in the token. Without both it returns ErrNoTrustTarget.
func Trust(settings *Settings) (origin, recordPath string, err error) {
	server, err := ResolveServer(settings)
	if err != nil {
		return "", "", err
	}

	serverURL := settings.ServerURL
	if !settings.ServerURLSet {
		if server.embeddedURL == "" {
			return "", "", fmt.Errorf("%w and no --%s was given", ErrNoTrustTarget, settings.ServerFlag)
		}

		serverURL = server.embeddedURL
	}

	origin, err = Origin(serverURL)
	if err != nil {
		return "", "", fmt.Errorf("resolve server URL: %w", err)
	}

	addErr := AddKnownOrigin(origin)
	if addErr != nil {
		return "", "", fmt.Errorf("record trusted server: %w", addErr)
	}

	recordPath, pathErr := HostsPath()
	if pathErr != nil {
		recordPath = "<config dir unavailable>"
	}

	return origin, recordPath, nil
}
