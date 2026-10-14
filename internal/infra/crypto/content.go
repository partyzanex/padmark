package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	contentVersion   = "pmc2"
	contentKind      = "p"
	legacyKind       = "l"
	contentParts     = 5
	contentNonceSize = 12
	contentTagSize   = 16
	wrappedKeySize   = contentNonceSize + aesKeySize + contentTagSize
	maxKeyIDSize     = 64
	// MaxContentSize bounds allocations when opening untrusted stored envelopes.
	MaxContentSize = 64 << 20
	//nolint:mnd // padded base64 length
	maxLegacyContentSize   = (MaxContentSize + contentNonceSize + contentTagSize + 2) / 3 * 4
	maxContentEnvelopeSize = 256 + 2*(maxLegacyContentSize+contentNonceSize+contentTagSize) // conservative base64 bound
)

// ContentCipher encrypts content with a fresh random DEK on every write, wrapping that DEK
// with the active installation key. IDs authenticate the row; they never supply key material.
// Instances are immutable after construction and safe for concurrent use.
type ContentCipher struct {
	keys     map[string]cipher.AEAD
	random   io.Reader
	activeID string
	resource string
}

// NewContentCipher binds a keyring to either "note" or "link". The keyring is copied into
// AES instances, so mutating the caller's map or byte slices cannot change a live cipher.
func NewContentCipher(activeID string, keys map[string][]byte, resource string) (*ContentCipher, error) {
	if resource != "note" && resource != "link" {
		return nil, errors.New("invalid content resource type")
	}

	if !validKeyID(activeID) || len(keys[activeID]) != aesKeySize {
		return nil, errors.New("active content key must identify a 32-byte key")
	}

	enc := &ContentCipher{
		keys: make(map[string]cipher.AEAD, len(keys)), random: rand.Reader,
		activeID: activeID, resource: resource,
	}
	for id, key := range keys {
		if !validKeyID(id) || len(key) != aesKeySize {
			return nil, errors.New("content keys require valid IDs and exactly 32 bytes")
		}

		gcm, err := contentGCM(key)
		if err != nil {
			return nil, err
		}

		enc.keys[id] = gcm
	}

	return enc, nil
}

func validKeyID(id string) bool {
	if id == "" || len(id) > maxKeyIDSize {
		return false
	}

	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}

	return true
}

//nolint:ireturn // stdlib cipher.AEAD is the GCM API
func contentGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("content AES: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("content GCM: %w", err)
	}

	return gcm, nil
}

func wrappedKeyAAD(aad []byte, keyID string, payload []byte) []byte {
	result := append([]byte("padmark-dek-v2\x00"), aad...)
	result = binary.BigEndian.AppendUint16(result, uint16(len(keyID))) //nolint:gosec // validated <= 64
	result = append(result, keyID...)
	digest := sha256.Sum256(payload)

	return append(result, digest[:]...)
}

// Encrypt writes pmc2$p$kid$wrappedDEK$payload. The supplied ID is the stored HashSlug ID,
// not the original slug. Empty content is valid and still gets an authenticated ciphertext.
func (enc *ContentCipher) Encrypt(plaintext, recordID string) (string, error) {
	return enc.encrypt(plaintext, recordID, contentKind)
}

func parseContentEnvelope(value string) (keyID string, wrapped, payload []byte, err error) {
	if len(value) > maxContentEnvelopeSize {
		return "", nil, nil, domain.ErrMalformedCiphertext
	}

	parts := strings.SplitN(value, "$", contentParts+1)
	if len(parts) != contentParts || parts[0] != contentVersion ||
		(parts[1] != contentKind && parts[1] != legacyKind) || !validKeyID(parts[2]) {
		return "", nil, nil, domain.ErrMalformedCiphertext
	}

	wrapped, err = decodeContentField(parts[3], wrappedKeySize, wrappedKeySize)
	if err != nil {
		return "", nil, nil, err
	}

	limit := MaxContentSize
	if parts[1] == legacyKind {
		limit = maxLegacyContentSize
	}

	payload, err = decodeContentField(parts[4], contentNonceSize+contentTagSize,
		limit+contentNonceSize+contentTagSize)
	if err != nil {
		return "", nil, nil, err
	}

	return parts[2], wrapped, payload, nil
}

// Decrypt opens only the new format, using the DB ID and resource type as authenticated
// context. It needs no slug. Unknown versions, kinds or keys fail closed, without fallback.
func (enc *ContentCipher) Decrypt(value, recordID string) (string, error) {
	plaintext, kind, err := enc.open(value, recordID)
	if err != nil {
		return "", err
	}

	if kind != contentKind {
		return "", domain.ErrMalformedCiphertext
	}

	return plaintext, nil
}

func (enc *ContentCipher) encrypt(plaintext, recordID, kind string) (string, error) {
	limit := MaxContentSize
	if kind == legacyKind {
		limit = maxLegacyContentSize
	}

	if len(plaintext) > limit {
		return "", errors.New("content exceeds encryption size limit")
	}

	aad, err := enc.contentAADForKind(recordID, kind)
	if err != nil {
		return "", err
	}

	dek := make([]byte, aesKeySize)

	_, err = io.ReadFull(enc.random, dek)
	if err != nil {
		return "", fmt.Errorf("content key: %w", err)
	}
	defer clear(dek)

	gcm, err := contentGCM(dek)
	if err != nil {
		return "", err
	}

	payload, err := enc.seal(gcm, []byte(plaintext), aad)
	if err != nil {
		return "", err
	}

	wrapped, err := enc.seal(enc.keys[enc.activeID], dek, wrappedKeyAAD(aad, enc.activeID, payload))
	if err != nil {
		return "", err
	}

	return strings.Join([]string{contentVersion, kind, enc.activeID,
		base64.RawURLEncoding.EncodeToString(wrapped), base64.RawURLEncoding.EncodeToString(payload)}, "$"), nil
}

func (enc *ContentCipher) open(value, recordID string) (plaintext, kind string, err error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", "", err
	}
	defer clear(envelope.dek)

	gcm, err := contentGCM(envelope.dek)
	if err != nil {
		return "", "", err
	}

	data, err := gcm.Open(nil, envelope.payload[:contentNonceSize], envelope.payload[contentNonceSize:], envelope.aad)
	if err != nil {
		return "", "", domain.ErrDecryptionFailed
	}

	return string(data), envelope.kind, nil
}

type unwrappedEnvelope struct {
	kind    string
	keyID   string
	dek     []byte
	payload []byte
	aad     []byte
}

// unwrap authenticates the complete payload digest and context without decrypting content.
func (enc *ContentCipher) unwrap(value, recordID string) (*unwrappedEnvelope, error) {
	keyID, wrapped, payload, err := parseContentEnvelope(value)
	if err != nil {
		return nil, err
	}

	kind := strings.SplitN(value, "$", contentParts)[1]

	kek, ok := enc.keys[keyID]
	if !ok {
		return nil, domain.ErrDecryptionFailed
	}

	aad, err := enc.contentAADForKind(recordID, kind)
	if err != nil {
		return nil, err
	}

	dek, err := kek.Open(nil, wrapped[:contentNonceSize], wrapped[contentNonceSize:], wrappedKeyAAD(aad, keyID, payload))
	if err != nil {
		return nil, domain.ErrDecryptionFailed
	}

	if len(dek) != aesKeySize {
		clear(dek)

		return nil, domain.ErrMalformedCiphertext
	}

	return &unwrappedEnvelope{dek: dek, payload: payload, aad: aad, kind: kind, keyID: keyID}, nil
}

// contentAAD has fixed-size fields: a purpose/version prefix, kind, resource byte, and
// the 32-byte database ID. Neither the human-readable slug nor a key derivation is involved.
func (enc *ContentCipher) contentAAD(recordID string) ([]byte, error) {
	return enc.contentAADForKind(recordID, contentKind)
}

func (enc *ContentCipher) contentAADForKind(recordID, kind string) ([]byte, error) {
	if len(recordID) != sha256.Size*2 {
		return nil, errors.New("content record ID must be a SHA-256 digest")
	}

	id, err := hex.DecodeString(recordID)
	if err != nil {
		return nil, errors.New("invalid content record ID")
	}

	resource := byte(1)
	if enc.resource == "link" {
		resource = 2
	}

	aad := append([]byte("padmark-content-v2\x00"+kind), resource)

	return append(aad, id...), nil
}

func (enc *ContentCipher) seal(gcm cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, gcm.NonceSize())

	_, err := io.ReadFull(enc.random, nonce)
	if err != nil {
		return nil, fmt.Errorf("content nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// ContentEncryptor adapts the envelope cipher to the notes/links usecase ports.
// Raw legacy ciphertext is only accepted by the explicit migration API, never by normal reads.
type ContentEncryptor struct{ *ContentCipher }

func (enc *ContentEncryptor) Decrypt(value, recordID, legacySlug string) (string, error) {
	plaintext, kind, err := enc.open(value, recordID)
	if err != nil {
		return "", err
	}

	if kind == contentKind {
		return plaintext, nil
	}

	if domain.HashSlug(legacySlug) != recordID {
		return "", domain.ErrDecryptionFailed
	}

	return New().Decrypt(plaintext, legacySlug)
}

// decodeContentField checks bounds before allocation and requires canonical unpadded base64url.
func decodeContentField(encoded string, minSize, maxSize int) ([]byte, error) {
	encoding := base64.RawURLEncoding.Strict()
	if len(encoded) > encoding.EncodedLen(maxSize) {
		return nil, domain.ErrMalformedCiphertext
	}

	data, err := encoding.DecodeString(encoded)
	if err != nil || len(data) < minSize || len(data) > maxSize || encoding.EncodeToString(data) != encoded {
		return nil, domain.ErrMalformedCiphertext
	}

	return data, nil
}
