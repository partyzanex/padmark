package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// contentVersionTag prefixes every envelope version tag ("pmc" + decimal digits).
	contentVersionTag = "pmc"
	contentVersion    = contentVersionTag + "2"
	contentKind       = "p"
	legacyKind        = "l"
	contentParts      = 5
	contentNonceSize  = 12
	contentTagSize    = 16
	wrappedKeySize    = contentNonceSize + aesKeySize + contentTagSize
	maxKeyIDSize      = 64
	// MaxContentSize bounds allocations when opening untrusted stored envelopes.
	MaxContentSize = 64 << 20
	// Padded base64 encodes every started block of 3 bytes as 4 characters.
	base64BlockBytes       = 3
	base64BlockChars       = 4
	maxLegacyPlaintext     = MaxContentSize + contentNonceSize + contentTagSize
	maxLegacyContentSize   = (maxLegacyPlaintext + base64BlockBytes - 1) / base64BlockBytes * base64BlockChars
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
		return nil, errInvalidContentResource
	}

	if !validKeyID(activeID) || len(keys[activeID]) != aesKeySize {
		return nil, errInvalidActiveContentKey
	}

	enc := &ContentCipher{
		keys: make(map[string]cipher.AEAD, len(keys)), random: rand.Reader,
		activeID: activeID, resource: resource,
	}
	for id, key := range keys {
		if !validKeyID(id) || len(key) != aesKeySize {
			return nil, errInvalidContentKeys
		}

		gcm, err := contentGCM(key)
		if err != nil {
			return nil, fmt.Errorf("content gcm: %w", err)
		}

		enc.keys[id] = gcm
	}

	return enc, nil
}

// IsVersionedContent reports whether value claims to be a versioned content envelope: the
// segment before the first "$" is a version tag "pmc" followed by decimal digits ("pmc2$…",
// or an unknown future "pmc9$…"). Legacy raw ciphertext is canonical standard base64 and can
// never contain "$", so any other value — including old plaintext rows with "$" in them —
// is unversioned raw data. Classification is purely syntactic: a versioned value may still
// fail authentication, and such values must never be treated as malformed raw records.
func IsVersionedContent(value string) bool {
	tag, _, found := strings.Cut(value, "$")
	if !found {
		return false
	}

	digits, ok := strings.CutPrefix(tag, contentVersionTag)
	if !ok || digits == "" {
		return false
	}

	for _, char := range digits {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
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

// wrappedKeyAAD binds the wrapped DEK to the record AAD, the key ID and the payload digest.
// Cipher construction already limits key IDs to maxKeyIDSize; the check repeats that bound
// where the length narrows to uint16.
func wrappedKeyAAD(aad []byte, keyID string, payload []byte) ([]byte, error) {
	keyIDSize := len(keyID)
	if keyIDSize > maxKeyIDSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalidContentKeyID, keyIDSize)
	}

	result := append([]byte("padmark-dek-v2\x00"), aad...)
	result = binary.BigEndian.AppendUint16(result, uint16(keyIDSize))
	result = append(result, keyID...)
	digest := sha256.Sum256(payload)

	return append(result, digest[:]...), nil
}

// Encrypt writes pmc2$p$kid$wrappedDEK$payload. The supplied ID is the stored HashSlug ID,
// not the original slug. Empty content is valid and still gets an authenticated ciphertext.
func (enc *ContentCipher) Encrypt(plaintext, recordID string) (string, error) {
	operationResult, operationErr := enc.encrypt(plaintext, recordID, contentKind)
	if operationErr != nil {
		return operationResult, fmt.Errorf("encrypt: %w", operationErr)
	}

	return operationResult, nil
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
		return "", nil, nil, fmt.Errorf("decode content field: %w", err)
	}

	limit := MaxContentSize
	if parts[1] == legacyKind {
		limit = maxLegacyContentSize
	}

	payload, err = decodeContentField(parts[4], contentNonceSize+contentTagSize,
		limit+contentNonceSize+contentTagSize)
	if err != nil {
		return "", nil, nil, fmt.Errorf("decode content field: %w", err)
	}

	return parts[2], wrapped, payload, nil
}

// Decrypt opens only the new format, using the DB ID and resource type as authenticated
// context. It needs no slug. Unknown versions, kinds or keys fail closed, without fallback.
func (enc *ContentCipher) Decrypt(value, recordID string) (string, error) {
	plaintext, kind, err := enc.open(value, recordID)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
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
		return "", errContentTooLarge
	}

	aad, err := enc.contentAADForKind(recordID, kind)
	if err != nil {
		return "", fmt.Errorf("content aadfor kind: %w", err)
	}

	dek := make([]byte, aesKeySize)

	_, err = io.ReadFull(enc.random, dek)
	if err != nil {
		return "", fmt.Errorf("content key: %w", err)
	}
	defer clear(dek)

	gcm, err := contentGCM(dek)
	if err != nil {
		return "", fmt.Errorf("content gcm: %w", err)
	}

	payload, err := enc.seal(gcm, []byte(plaintext), aad)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}

	keyAAD, err := wrappedKeyAAD(aad, enc.activeID, payload)
	if err != nil {
		return "", fmt.Errorf("wrapped key aad: %w", err)
	}

	wrapped, err := enc.seal(enc.keys[enc.activeID], dek, keyAAD)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}

	return strings.Join([]string{contentVersion, kind, enc.activeID,
		base64.RawURLEncoding.EncodeToString(wrapped), base64.RawURLEncoding.EncodeToString(payload)}, "$"), nil
}

func (enc *ContentCipher) open(value, recordID string) (plaintext, kind string, err error) {
	envelope, err := enc.unwrap(value, recordID)
	if err != nil {
		return "", "", fmt.Errorf("unwrap: %w", err)
	}
	defer clear(envelope.dek)

	gcm, err := contentGCM(envelope.dek)
	if err != nil {
		return "", "", fmt.Errorf("content gcm: %w", err)
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
		return nil, fmt.Errorf("parse content envelope: %w", err)
	}

	kind := strings.SplitN(value, "$", contentParts)[1]

	kek, ok := enc.keys[keyID]
	if !ok {
		return nil, domain.ErrDecryptionFailed
	}

	aad, err := enc.contentAADForKind(recordID, kind)
	if err != nil {
		return nil, fmt.Errorf("content aadfor kind: %w", err)
	}

	keyAAD, err := wrappedKeyAAD(aad, keyID, payload)
	if err != nil {
		return nil, domain.ErrDecryptionFailed
	}

	dek, err := kek.Open(nil, wrapped[:contentNonceSize], wrapped[contentNonceSize:], keyAAD)
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
	operationResult, operationErr := enc.contentAADForKind(recordID, contentKind)
	if operationErr != nil {
		return operationResult, fmt.Errorf("content aadfor kind: %w", operationErr)
	}

	return operationResult, nil
}

// AAD prevents swaps between IDs/resources/kinds, but has no monotonic revision.
// A database writer can replay an older valid envelope for the same record; at-rest
// encryption does not provide freshness without a trusted version store outside the DB.
func (enc *ContentCipher) contentAADForKind(recordID, kind string) ([]byte, error) {
	if len(recordID) != sha256.Size*2 {
		return nil, errInvalidContentRecordIDSize
	}

	id, err := hex.DecodeString(recordID)
	if err != nil {
		return nil, errInvalidContentRecordID
	}

	resource := byte(1)
	if enc.resource == "link" {
		resource = 2
	}

	aad := append([]byte("padmark-content-v2\x00"+kind), resource)

	return append(aad, id...), nil
}

// seal uses random 96-bit GCM nonces. KEKs are reused across wrapped DEKs/key checks:
// collision probability grows quadratically with aggregate seals on all replicas.
// This format targets low-volume installations; keep the aggregate number of seals
// per KEK well below 2^32 and rotate KEKs before approaching that bound.
// A future format for high-volume use needs per-record wrapping subkeys.
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
		return "", fmt.Errorf("open: %w", err)
	}

	if kind == contentKind {
		return plaintext, nil
	}

	// Normal reads derive recordID from the same slug: GCM with the slug-derived
	// key is the actual authentication. The explicit check also protects migration
	// callers that can supply the record ID and original slug independently.
	if domain.HashSlug(legacySlug) != recordID {
		return "", domain.ErrDecryptionFailed
	}

	operationResult, operationErr := New().Decrypt(plaintext, legacySlug)
	if operationErr != nil {
		return operationResult, fmt.Errorf("decrypt: %w", operationErr)
	}

	return operationResult, nil
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
