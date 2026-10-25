package crypto

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AddKey adds a random 256-bit KEK; reusing a key ID is rejected.
func (k *ContentKeyring) AddKey(id string) error {
	if !validKeyID(id) {
		return ErrInvalidContentKeyID
	}

	if _, exists := k.Keys[id]; exists {
		return ErrContentKeyIDExists
	}

	key := make([]byte, aesKeySize)

	_, randomErr := rand.Read(key)
	if randomErr != nil {
		return fmt.Errorf("generate content key: %w", randomErr)
	}

	if k.Keys == nil {
		k.Keys = make(map[string][]byte)
	}

	k.Keys[id] = key

	return nil
}

// WriteContentKeyring publishes a complete 0600 file exclusively, never overwriting a backup.
// A temporary file in the same directory is fsynced before linking it into its final name.
func WriteContentKeyring(path string, keyring *ContentKeyring) error {
	_, validateErr := NewContentCipher(keyring.Active, keyring.Keys, "note")
	if validateErr != nil {
		return fmt.Errorf("new content cipher: %w", validateErr)
	}

	data, err := json.Marshal(keyring)
	if err != nil {
		return errEncodeKeyring
	}
	defer clear(data)

	if len(data) > maxKeyringSize {
		return errKeyringTooLarge
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".padmark-keyring-*")
	if err != nil {
		return fmt.Errorf("create keyring file: %w", err)
	}
	defer os.Remove(file.Name()) //nolint:errcheck // best-effort removal of unpublished temporary file

	_, writeErr := file.Write(data)
	syncErr := file.Sync()

	closeErr := file.Close()

	persistErr := errors.Join(writeErr, syncErr, closeErr)
	if persistErr != nil {
		return fmt.Errorf("persist content keyring: %w", persistErr)
	}

	publishErr := os.Link(file.Name(), path)
	if publishErr != nil {
		return fmt.Errorf("publish keyring without overwrite: %w", publishErr)
	}

	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open keyring directory: %w", err)
	}

	syncErr = dir.Sync()

	closeErr = dir.Close()

	directoryErr := errors.Join(syncErr, closeErr)
	if directoryErr != nil {
		return fmt.Errorf("persist keyring directory: %w", directoryErr)
	}

	return nil
}
