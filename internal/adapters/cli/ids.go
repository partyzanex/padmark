package cli

import (
	"fmt"
	"net/url"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
)

// recordIDArg returns the record ID from the first positional argument. For a file, the
// argument can also be the file URL that `upload` prints: /{id}, /files/{id} or
// /files/{id}/download. The CLI sends the request to --url, so a URL argument must have the
// same origin as --url. Otherwise `delete-file https://other.example/x` would delete the file
// "x" on the configured server.
func recordIDArg(cmd *urcli.Command, kind *recordKind) (string, error) {
	arg := strings.TrimSpace(cmd.Args().Get(0))
	if arg == "" {
		return "", kind.idRequired
	}

	if !kind.fileURLs || !strings.Contains(arg, "://") {
		return checkRecordID(arg)
	}

	serverURL, err := resolveServerURL(cmd)
	if err != nil {
		return "", err
	}

	err = checkFileURLOrigin(arg, serverURL)
	if err != nil {
		return "", err
	}

	id, err := fileIDFromURL(arg)
	if err != nil {
		return "", err
	}

	return checkRecordID(id)
}

// checkRecordID refuses "." and "..". The client puts the ID in a URL path, where these values
// change the route.
func checkRecordID(id string) (string, error) {
	if id == "." || id == ".." {
		return "", fmt.Errorf("%w: %q", errInvalidID, id)
	}

	return id, nil
}

// checkFileURLOrigin compares the origin (scheme, host and port) of a file URL with the origin
// of the server URL.
func checkFileURLOrigin(fileURL, serverURL string) error {
	fileOrigin, err := clientauth.Origin(fileURL)
	if err != nil {
		return fmt.Errorf("%w: %w", errFileURL, err)
	}

	serverOrigin, err := clientauth.Origin(serverURL)
	if err != nil {
		return fmt.Errorf("resolve server URL: %w", err)
	}

	if fileOrigin != serverOrigin {
		return fmt.Errorf("%w: the URL is on %s, the server is %s; pass --url %s",
			errFileURLOrigin, fileOrigin, serverOrigin, fileOrigin)
	}

	return nil
}

// fileIDFromURL reads the file ID from the path of a file URL.
func fileIDFromURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errFileURL, err)
	}

	id := strings.Trim(parsed.Path, "/")
	if rest, ok := strings.CutPrefix(id, filesPathSegment+"/"); ok {
		id = strings.TrimSuffix(rest, "/download")
	}

	if id == "" || id == filesPathSegment || strings.Contains(id, "/") {
		return "", fmt.Errorf("%w: %s", errFileURL, raw)
	}

	return id, nil
}
