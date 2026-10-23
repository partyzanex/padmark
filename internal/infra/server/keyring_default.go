package server

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

const (
	storageSQLite = "sqlite"

	// defaultKeyringName is the keyring file placed next to the SQLite database file.
	defaultKeyringName = "padmark-keys.json"

	// defaultKeyID names the single key of an automatically generated keyring.
	defaultKeyID = "k1"
)

// errKeyringRequired explains how to provide a keyring when no default can be derived.
var errKeyringRequired = errors.New("content keyring is required: set --" + FlagContentKeyring + " (" +
	EnvContentKeyring + ") to a keyring created with `padmark-server encryption keygen`; " +
	"the sqlite backend defaults to <db dir>/" + defaultKeyringName + " when the database is a file")

// defaultKeyringPath derives <db dir>/padmark-keys.json for a file-backed SQLite database.
// It returns "" for other backends and for in-memory or temporary SQLite databases.
func defaultKeyringPath(storage, dsn string) string {
	if storage != storageSQLite {
		return ""
	}

	dbPath, _ := sqlite.DBFilePath(dsn)
	if dbPath == "" {
		return ""
	}

	return filepath.Join(filepath.Dir(dbPath), defaultKeyringName)
}

// resolveKeyringPath returns the explicit path, or the SQLite default (derived=true).
// Fail-hard: without either there is no keyring and startup must stop.
func resolveKeyringPath(explicit, storage, dsn string) (path string, derived bool, err error) {
	if explicit != "" {
		return explicit, false, nil
	}

	path = defaultKeyringPath(storage, dsn)
	if path == "" {
		return "", false, errKeyringRequired
	}

	return path, true, nil
}

// loadServeKeyring loads the keyring for serve. A derived default path that does not exist
// yet is created with one fresh key; explicit paths are never created.
func loadServeKeyring(explicit, storage, dsn string, log *slog.Logger) (*crypto.ContentKeyring, error) {
	path, derived, err := resolveKeyringPath(explicit, storage, dsn)
	if err != nil {
		return nil, err
	}

	if derived {
		err = ensureDefaultKeyring(path, log)
		if err != nil {
			return nil, err
		}
	}

	return loadContentKeyring(path, false, log)
}

// loadToolKeyring loads the keyring for the encryption subcommands; it never creates one.
func loadToolKeyring(explicit, storage, dsn string, allowFlag bool, log *slog.Logger) (*crypto.ContentKeyring, error) {
	path, _, err := resolveKeyringPath(explicit, storage, dsn)
	if err != nil {
		return nil, err
	}

	return loadContentKeyring(path, allowFlag, log)
}

// ensureDefaultKeyring creates the installation keyring at path when it is missing.
// WriteContentKeyring never overwrites, so a concurrent creator cannot lose its key.
func ensureDefaultKeyring(path string, log *slog.Logger) error {
	_, statErr := os.Lstat(path)
	if statErr == nil {
		return nil
	}

	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat default content keyring: %w", statErr)
	}

	keyring := &crypto.ContentKeyring{Active: defaultKeyID, Keys: make(map[string][]byte)}

	err := keyring.AddKey(defaultKeyID)
	if err != nil {
		return fmt.Errorf("generate default content keyring: %w", err)
	}

	err = crypto.WriteContentKeyring(path, keyring)
	if err != nil {
		return fmt.Errorf("create default content keyring: %w", err)
	}

	log.Warn("generated installation content key; back it up separately from the database, "+
		"losing this file makes all encrypted content unrecoverable",
		slog.String("path", path), slog.String("key_id", defaultKeyID))

	return nil
}
