package crypto

import (
	"errors"
)

// Errors of content encryption and keyring handling. Compare the exported values with errors.Is.
var (
	ErrContentKeyIDExists      = errors.New("content key ID already exists")
	ErrContentKeyMismatch      = errors.New("content key does not match this installation")
	ErrContentKeyMissing       = errors.New("required content key is missing")
	ErrInvalidContentKeyID     = errors.New("invalid content key ID")
	errContentTooLarge         = errors.New("content exceeds encryption size limit")
	errEncodeKeyring           = errors.New("encode content keyring")
	errInvalidActiveContentKey = errors.New("active content key must identify a 32-byte key")
	errInvalidArgon2Cost       = errors.New("argon2 cost outside supported bounds (memory: " +
		"8*threads..262144 KiB, time: 1..10, threads: " +
		"1..16)")
	errInvalidArgon2Threads        = errors.New("invalid argon2 threads")
	errInvalidContentKeys          = errors.New("content keys require valid IDs and exactly 32 bytes")
	errInvalidContentRecordID      = errors.New("invalid content record ID")
	errInvalidContentRecordIDSize  = errors.New("content record ID must be a SHA-256 digest")
	errInvalidContentResource      = errors.New("invalid content resource type")
	errInvalidKeyVerification      = errors.New("invalid content key verification")
	errInvalidKeyringDocument      = errors.New("content keyring must contain one JSON object within 64 KiB")
	errInvalidKeyringJSON          = errors.New("invalid content keyring JSON")
	errInvalidSealedSecret         = errors.New("invalid sealed secret envelope or unsupported argon2 cost")
	errKeyringFileRequired         = errors.New("content keyring file is required")
	errKeyringTooLarge             = errors.New("content keyring exceeds 64 KiB")
	errKeyringUnreadableOrTooLarge = errors.New("content keyring must be readable and within 64 KiB")
	errMissingOrInvalidKey         = errors.New("key is missing or invalid")
	errSealedSecretTooLarge        = errors.New("sealed secret exceeds size limit")
)
