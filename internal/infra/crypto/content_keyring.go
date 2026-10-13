package crypto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxKeyringSize = 64 << 10

// ContentKeyring holds installation keys loaded from a secret file, never from the database.
// JSON byte slices use standard base64. Keep the file outside the Docker build context.
type ContentKeyring struct {
	Keys   map[string][]byte `json:"keys"`
	Active string            `json:"active"`
}

// LoadContentKeyring fails on missing, malformed or invalid keys. It never generates a key
// or prints input values in an error; the same persisted keyring is required after restart.
func LoadContentKeyring(path string) (*ContentKeyring, error) {
	if path == "" {
		return nil, errors.New("content keyring file is required")
	}

	file, err := os.Open(path) //nolint:gosec // explicit operator-supplied secret path
	if err != nil {
		return nil, fmt.Errorf("open content keyring: %w", err)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxKeyringSize+1))

	closeErr := file.Close()
	if err != nil || closeErr != nil || len(data) > maxKeyringSize {
		return nil, errors.New("content keyring must be readable and within 64 KiB")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var keyring ContentKeyring

	err = decoder.Decode(&keyring)
	if err != nil {
		return nil, errors.New("invalid content keyring JSON")
	}

	var extra any
	if decoder.Decode(&extra) != io.EOF || decoder.InputOffset() > maxKeyringSize {
		return nil, errors.New("content keyring must contain one JSON object within 64 KiB")
	}

	_, err = NewContentCipher(keyring.Active, keyring.Keys, "note")
	if err != nil {
		return nil, err
	}

	return &keyring, nil
}

// ForResource constructs an independent cipher for a note or link from this keyring.
func (k *ContentKeyring) ForResource(resource string) (*ContentEncryptor, error) {
	content, err := NewContentCipher(k.Active, k.Keys, resource)
	if err != nil {
		return nil, err
	}

	return &ContentEncryptor{ContentCipher: content}, nil
}
