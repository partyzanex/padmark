package crypto

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

// Versioned classifies value with IsVersionedContent; see its godoc for the exact rule.
func (enc *ContentCipher) Versioned(value string) bool {
	return IsVersionedContent(value)
}

// ValidateLegacy checks structure only: the original slug is required to authenticate legacy data.
func (enc *ContentCipher) ValidateLegacy(value string) error {
	if len(value) > maxLegacyContentSize {
		return domain.ErrMalformedCiphertext
	}

	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < contentNonceSize+contentTagSize ||
		len(decoded) > MaxContentSize+contentNonceSize+contentTagSize ||
		base64.StdEncoding.EncodeToString(decoded) != value {
		return domain.ErrMalformedCiphertext
	}

	return nil
}

// WrapLegacy protects an existing ciphertext without knowing or recovering its slug.
func (enc *ContentCipher) WrapLegacy(value, recordID string) (string, error) {
	err := enc.ValidateLegacy(value)
	if err != nil {
		return "", fmt.Errorf("validate legacy: %w", err)
	}

	operationResult, operationErr := enc.encrypt(value, recordID, legacyKind)
	if operationErr != nil {
		return operationResult, fmt.Errorf("encrypt: %w", operationErr)
	}

	return operationResult, nil
}

// Inspect authenticates the envelope, including its payload digest, without opening the payload.
func (enc *ContentCipher) Inspect(value, recordID string) (kind, keyID string, err error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", "", fmt.Errorf("unwrap: %w", err)
	}

	clear(envelope.dek)

	return envelope.kind, envelope.keyID, nil
}

// Rewrap changes only the wrapped DEK and key ID; the payload and its nonce remain identical.
func (enc *ContentCipher) Rewrap(value, recordID string) (string, error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", fmt.Errorf("unwrap: %w", err)
	}
	defer clear(envelope.dek)

	if envelope.keyID == enc.activeID {
		return value, nil
	}

	keyAAD, err := wrappedKeyAAD(envelope.aad, enc.activeID, envelope.payload)
	if err != nil {
		return "", fmt.Errorf("wrapped key aad: %w", err)
	}

	wrapped, err := enc.seal(enc.keys[enc.activeID], envelope.dek, keyAAD)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}

	return strings.Join([]string{
		contentVersion, envelope.kind, enc.activeID,
		base64.RawURLEncoding.EncodeToString(wrapped), base64.RawURLEncoding.EncodeToString(envelope.payload),
	}, "$"), nil
}

// KeyCheck creates a registry value with its own purpose and installation/key binding.
func (k *ContentKeyring) KeyCheck(installationID, keyID string) (string, error) {
	if !validKeyID(keyID) || len(k.Keys[keyID]) != aesKeySize {
		return "", errMissingOrInvalidKey
	}

	gcm, err := contentGCM(k.Keys[keyID])
	if err != nil {
		return "", fmt.Errorf("content gcm: %w", err)
	}

	enc, err := NewContentCipher(k.Active, k.Keys, "note")
	if err != nil {
		return "", fmt.Errorf("new content cipher: %w", err)
	}

	sealed, err := enc.seal(gcm, []byte("padmark-key-check"), keyCheckAAD(installationID, keyID))
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// VerifyKeyCheck rejects a missing or substituted key and never replaces a failed check.
func (k *ContentKeyring) VerifyKeyCheck(installationID, keyID, value string) error {
	if !validKeyID(keyID) || len(k.Keys[keyID]) != aesKeySize {
		return ErrContentKeyMissing
	}

	data, err := decodeContentField(value, contentNonceSize+contentTagSize, keyCheckSize)
	if err != nil {
		return errInvalidKeyVerification
	}

	gcm, err := contentGCM(k.Keys[keyID])
	if err != nil {
		return fmt.Errorf("content gcm: %w", err)
	}

	plain, err := gcm.Open(nil, data[:contentNonceSize], data[contentNonceSize:], keyCheckAAD(installationID, keyID))
	if err != nil || string(plain) != "padmark-key-check" {
		return ErrContentKeyMismatch
	}

	return nil
}

const keyCheckSize = contentNonceSize + contentTagSize + len("padmark-key-check")

func keyCheckAAD(installationID, keyID string) []byte {
	return []byte("padmark-key-check-v1\x00" + installationID + "\x00" + keyID)
}

// ActiveKey returns the ID of the key that encrypts new content.
func (k *ContentKeyring) ActiveKey() string { return k.Active }

// KeyIDs returns the IDs of all keys in the keyring, in no specific order.
func (k *ContentKeyring) KeyIDs() []string {
	ids := make([]string, 0, len(k.Keys))
	for id := range k.Keys {
		ids = append(ids, id)
	}

	return ids
}
