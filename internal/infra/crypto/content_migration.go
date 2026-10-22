package crypto

import (
	"encoding/base64"
	"errors"
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
		return "", err
	}

	return enc.encrypt(value, recordID, legacyKind)
}

// Inspect authenticates the envelope, including its payload digest, without opening the payload.
func (enc *ContentCipher) Inspect(value, recordID string) (kind, keyID string, err error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", "", err
	}

	clear(envelope.dek)

	return envelope.kind, envelope.keyID, nil
}

// Rewrap changes only the wrapped DEK and key ID; the payload and its nonce remain identical.
func (enc *ContentCipher) Rewrap(value, recordID string) (string, error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", err
	}
	defer clear(envelope.dek)

	if envelope.keyID == enc.activeID {
		return value, nil
	}

	wrapped, err := enc.seal(enc.keys[enc.activeID], envelope.dek,
		wrappedKeyAAD(envelope.aad, enc.activeID, envelope.payload))
	if err != nil {
		return "", err
	}

	return strings.Join([]string{contentVersion, envelope.kind, enc.activeID,
		base64.RawURLEncoding.EncodeToString(wrapped), base64.RawURLEncoding.EncodeToString(envelope.payload)}, "$"), nil
}

// KeyCheck creates a registry value with its own purpose and installation/key binding.
func (k *ContentKeyring) KeyCheck(installationID, keyID string) (string, error) {
	if !validKeyID(keyID) || len(k.Keys[keyID]) != aesKeySize {
		return "", errors.New("key is missing or invalid")
	}

	gcm, err := contentGCM(k.Keys[keyID])
	if err != nil {
		return "", err
	}

	enc, err := NewContentCipher(k.Active, k.Keys, "note")
	if err != nil {
		return "", err
	}

	sealed, err := enc.seal(gcm, []byte("padmark-key-check"), keyCheckAAD(installationID, keyID))
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// VerifyKeyCheck rejects a missing or substituted key and never replaces a failed check.
func (k *ContentKeyring) VerifyKeyCheck(installationID, keyID, value string) error {
	if !validKeyID(keyID) || len(k.Keys[keyID]) != aesKeySize {
		return errors.New("required content key is missing")
	}

	data, err := decodeContentField(value, contentNonceSize+contentTagSize, keyCheckSize)
	if err != nil {
		return errors.New("invalid content key verification")
	}

	gcm, err := contentGCM(k.Keys[keyID])
	if err != nil {
		return err
	}

	plain, err := gcm.Open(nil, data[:contentNonceSize], data[contentNonceSize:], keyCheckAAD(installationID, keyID))
	if err != nil || string(plain) != "padmark-key-check" {
		return errors.New("content key does not match this installation")
	}

	return nil
}

const keyCheckSize = contentNonceSize + contentTagSize + len("padmark-key-check")

func keyCheckAAD(installationID, keyID string) []byte {
	return []byte("padmark-key-check-v1\x00" + installationID + "\x00" + keyID)
}

func (k *ContentKeyring) ActiveKey() string { return k.Active }

func (k *ContentKeyring) KeyIDs() []string {
	ids := make([]string, 0, len(k.Keys))
	for id := range k.Keys {
		ids = append(ids, id)
	}

	return ids
}
