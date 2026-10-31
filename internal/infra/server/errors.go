package server

import (
	"errors"
)

var (
	errActivateUnregisteredKey = errors.New("initialize and register the key before activation")
	errIncompleteTLSConfig     = errors.New("--tls-cert and --tls-key must both be set to enable TLS")
	errKeyIDNotInKeyring       = errors.New("key ID not present in keyring")
	errKeyringOutputRequired   = errors.New("--output must name a new keyring file outside the repository")
	errMarkLegacyRequiresID    = errors.New("mark-legacy requires --id; list candidate IDs " +
		"with prune --corrupt --dry-run")
	errPruneRequiresCorrupt = errors.New("prune requires --corrupt; preview permanent " +
		"raw-record deletion with --corrupt --dry-run")
	errUnknownEncryptionOperation = errors.New("unknown encryption operation")
	errUnsupportedDryRun          = errors.New("--dry-run is supported only by migrate, " +
		"rewrap, convert and prune; status is always " +
		"read-only")
)

var (
	errFlagOutOfRange        = errors.New("flag value out of range")
	errInvalidArgon2Budget   = errors.New("insufficient argon2 memory budget")
	errInvalidArgon2Cost     = errors.New("cost must be between")
	errInvalidBootstrapToken = errors.New("invalid bootstrap token length")
	errInvalidCSRFSecret     = errors.New("invalid csrf secret length")
	errInvalidPublicScheme   = errors.New("invalid public scheme")
	errInvalidTrustedProxy   = errors.New("invalid trusted proxy address")
	errUnknownStorage        = errors.New("unknown storage backend")
)
