package crypto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

const maxKeyringSize = 64 << 10

var errContentKeyringNotRegular = errors.New("content keyring must be a regular file")

// ErrKeyringPermissions reports a keyring file that group or other users may access. The
// keyring is the root of trust for content at rest, so the loader refuses it by default.
var ErrKeyringPermissions = errors.New("content keyring is accessible to group or other users")

// KeyringLoadOptions tunes how LoadContentKeyring vets the keyring file.
type KeyringLoadOptions struct {
	// AllowSharedPermissions accepts a keyring that group or other users may access and only
	// logs a warning. Use it for secret managers that cannot provide an owner-only file.
	AllowSharedPermissions bool
}

// ContentKeyring holds installation keys loaded from a secret file, never from the database.
// JSON byte slices use standard base64. Keep the file outside the Docker build context.
type ContentKeyring struct {
	Keys   map[string][]byte `json:"keys"`
	Active string            `json:"active"`
}

// LoadContentKeyring fails on missing, malformed or invalid keys. It never generates a key
// or prints input values in an error; the same persisted keyring is required after restart.
// A file accessible to group or other users fails with ErrKeyringPermissions unless
// opts.AllowSharedPermissions is set; that exception and symlinks are logged through log
// at WARN. The loader never changes the permissions of the operator's file.
func LoadContentKeyring(path string, log *slog.Logger, opts KeyringLoadOptions) (*ContentKeyring, error) {
	if path == "" {
		return nil, errKeyringFileRequired
	}

	err := requireRegularKeyring(path)
	if err != nil {
		return nil, fmt.Errorf("require regular keyring: %w", err)
	}

	file, err := os.Open(path) //nolint:gosec // explicit operator-supplied secret path
	if err != nil {
		return nil, fmt.Errorf("open content keyring: %w", err)
	}

	permissionErr := checkKeyringPermissions(path, file, log, opts)
	if permissionErr != nil {
		// Join keeps the permission error first and still reports a failed close.
		closeErr := file.Close()

		return nil, fmt.Errorf("check keyring permissions: %w", errors.Join(permissionErr, closeErr))
	}

	data, err := io.ReadAll(io.LimitReader(file, maxKeyringSize+1))

	closeErr := file.Close()
	if err != nil || closeErr != nil || len(data) > maxKeyringSize {
		return nil, errKeyringUnreadableOrTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var keyring ContentKeyring

	err = decoder.Decode(&keyring)
	if err != nil {
		return nil, errInvalidKeyringJSON
	}

	var extra any
	if decoder.Decode(&extra) != io.EOF || decoder.InputOffset() > maxKeyringSize {
		return nil, errInvalidKeyringDocument
	}

	_, err = NewContentCipher(keyring.Active, keyring.Keys, "note")
	if err != nil {
		return nil, fmt.Errorf("new content cipher: %w", err)
	}

	return &keyring, nil
}

// requireRegularKeyring rejects FIFOs, devices and directories before opening: opening
// a FIFO blocks until a writer appears. The post-open fstat check in
// checkKeyringPermissions still guards the opened descriptor against a swap between
// this lookup and the open. Stat follows symlinks, so managed secret mounts still work.
func requireRegularKeyring(path string) error {
	target, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat content keyring: %w", err)
	}

	if !target.Mode().IsRegular() {
		return errContentKeyringNotRegular
	}

	return nil
}

// checkKeyringPermissions inspects the opened file, not a second lookup of the target.
// Symlinks remain supported for managed secret mounts, but their use is logged; no
// contents or key bytes enter the log or the error.
func checkKeyringPermissions(path string, file *os.File, log *slog.Logger, opts KeyringLoadOptions) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat opened content keyring: %w", err)
	}

	if !info.Mode().IsRegular() {
		return errContentKeyringNotRegular
	}

	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		if !opts.AllowSharedPermissions {
			return fmt.Errorf("content keyring %s has mode %04o, readable by other users; restrict it to 0600 or 0400: %w",
				path, uint32(perm), ErrKeyringPermissions)
		}

		log.Warn("content keyring is readable by other users; shared permissions explicitly allowed",
			"path", path, "mode", fmt.Sprintf("%04o", uint32(perm)))
	}

	entry, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat content keyring: %w", err)
	}

	if entry.Mode()&os.ModeSymlink != 0 {
		log.Warn("content keyring uses a symlink; ensure its directory and target are trusted", "path", path)
	}

	return nil
}

// ForResource constructs an independent cipher for a note or link from this keyring.
func (k *ContentKeyring) ForResource(resource string) (*ContentEncryptor, error) {
	content, err := NewContentCipher(k.Active, k.Keys, resource)
	if err != nil {
		return nil, fmt.Errorf("new content cipher: %w", err)
	}

	return &ContentEncryptor{ContentCipher: content}, nil
}
