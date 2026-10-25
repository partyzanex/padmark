package tokenfile

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// configDirMode is the permission for the config dir holding the token and hosts files.
const configDirMode os.FileMode = 0o700

// knownHostsFile is the trust-on-first-use record next to the token file: one trusted server
// origin ("scheme://host[:port]") per line. A pmk_ envelope token carries the server base URL
// it wants traffic sent to, and that URL is attacker-controllable when the token comes from a
// third party — so the CLI and MCP binaries refuse to use a pinned origin until it appears
// here (see the trust commands in both adapters). Like the token file it lives under the
// user's config dir and is owner-only.
const knownHostsFile = "known_hosts"

// HostsPath resolves ~/.config/padmark/known_hosts, honouring XDG_CONFIG_HOME when set —
// the same directory tokenfile.Path resolves the token file in.
func HostsPath() (string, error) {
	operationResult, operationErr := configPath(knownHostsFile)
	if operationErr != nil {
		return operationResult, fmt.Errorf("config path: %w", operationErr)
	}

	return operationResult, nil
}

// permGroupOtherWrite masks the group- and other-write bits. A trust store anyone else can
// write to is a trust store an attacker can append their own origin to.
const permGroupOtherWrite os.FileMode = 0o022

// defaultPorts maps the schemes whose default port is implied when absent, so an explicit
// default port and the bare host pin the same origin.
var defaultPorts = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"http":  "80",
	"https": "443",
}

// Origin reduces a server base URL to the canonical "scheme://host[:port]" string that trust
// is pinned to: the path never matters for where requests go, but the scheme and port do (an
// http:// pin must not silently trust an https:// one on the same host). The host is
// lowercased and a scheme's default port is dropped, so https://A.com:443 and https://a.com
// are the same origin.
func Origin(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())

	if scheme == "" || host == "" {
		return "", fmt.Errorf("server URL %q %w", serverURL, ErrInvalidServerURL)
	}

	port := parsed.Port()
	if port == defaultPorts[scheme] {
		port = ""
	}

	switch {
	case port != "":
		host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		host = "[" + host + "]" // IPv6 literal without a port keeps its brackets
	}

	return scheme + "://" + host, nil
}

// IsKnownOrigin reports whether origin has been recorded as trusted. Both the argument and
// every stored line are canonicalised through Origin before comparing, so records written
// before normalisation (or by hand) keep matching; lines that do not parse are skipped. It
// fails closed: a missing, unreadable, non-regular or group/other-writable hosts file means
// nothing is trusted — callers treat that as "confirm first", never as "allow". Running the
// trust command re-tightens the file to 0600 (AddKnownOrigin), which heals the mode.
func IsKnownOrigin(origin string) bool {
	want, err := Origin(origin)
	if err != nil {
		return false
	}

	data, ok := readHostsFile()
	if !ok {
		return false
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		stored, parseErr := Origin(strings.TrimSpace(line))
		if parseErr == nil && stored == want {
			return true
		}
	}

	return false
}

// readHostsFile returns the hosts file contents, or ok=false when the file must not be relied
// on. The checks run on the open descriptor, so the file inspected is the file read.
func readHostsFile() ([]byte, bool) {
	path, err := HostsPath()
	if err != nil {
		return nil, false
	}

	file, err := os.Open(path) //nolint:gosec // path derived from the user's own config dir
	if err != nil {
		return nil, false
	}

	defer file.Close() //nolint:errcheck // read-only descriptor; the read result is what matters

	info, err := file.Stat()
	if err != nil || !trustworthyHostsMode(info.Mode()) {
		return nil, false
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, false
	}

	return data, true
}

// trustworthyHostsMode reports whether a hosts file with this mode may be trusted: it must be
// a regular file and, outside Windows (where POSIX bits are not meaningful), writable by
// nobody but its owner.
func trustworthyHostsMode(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}

	return runtime.GOOS == osWindows || mode.Perm()&permGroupOtherWrite == 0
}

// AddKnownOrigin appends origin, canonicalised through Origin, to the hosts file, creating it
// owner-only (0600) next to the token file. An origin that is already recorded is not
// duplicated. The file is not a secret,
// but it decides where credentials are sent, so it stays owner-only like the token itself.
func AddKnownOrigin(origin string) error {
	if origin == "" {
		return errEmptyTrustedOrigin
	}

	origin, err := Origin(origin)
	if err != nil {
		return fmt.Errorf("normalise trusted origin: %w", err)
	}

	if IsKnownOrigin(origin) {
		return nil
	}

	path, err := HostsPath()
	if err != nil {
		return fmt.Errorf("hosts path: %w", err)
	}

	mkdirErr := os.MkdirAll(filepath.Dir(path), configDirMode)
	if mkdirErr != nil {
		return fmt.Errorf("create config dir: %w", mkdirErr)
	}

	//nolint:gosec // config-dir path, same pattern as token reads
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("open known hosts: %w", err)
	}

	defer file.Close() //nolint:errcheck // read-only close on an append-only descriptor; the write error is what matters

	_, writeErr := file.WriteString(origin + "\n")
	if writeErr != nil {
		return fmt.Errorf("record trusted origin: %w", writeErr)
	}

	// OpenFile's perm only applies to creation; a pre-existing file keeps its old mode, so
	// re-tighten exactly like token reads do.
	Secure(path, nil)

	return nil
}
