package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const argon2KeyLen uint32 = 32

const argon2SaltLen = 16

// defaultArgon2MemoryKiB is the built-in argon2id memory cost (64 MiB).
const defaultArgon2MemoryKiB uint32 = 64 * 1024

// defaultArgon2Time is the built-in argon2id iteration count (OWASP minimum at 64 MiB).
const defaultArgon2Time uint32 = 2

// Argon2Params holds tunable argon2id cost parameters shared by password and edit-code
// hashing. Memory is in KiB; raising memory or time increases brute-force cost (and CPU/RAM
// per hash). Cost depends on these params, not on input length.
type Argon2Params struct {
	Memory  uint32 // KiB (e.g. 65536 = 64 MiB)
	Time    uint32 // iterations
	Threads uint8  // parallelism (CPU cores)
}

// DefaultArgon2Params returns the built-in defaults (64 MiB, time=2, threads=1).
// time=2 follows the OWASP argon2id minimum at this memory cost.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: defaultArgon2MemoryKiB, Time: defaultArgon2Time, Threads: 1}
}

// withDefaults replaces any zero field with its default, so partial/unset config is safe.
func (p Argon2Params) withDefaults() Argon2Params {
	def := DefaultArgon2Params()

	if p.Memory == 0 {
		p.Memory = def.Memory
	}

	if p.Time == 0 {
		p.Time = def.Time
	}

	if p.Threads == 0 {
		p.Threads = def.Threads
	}

	return p
}

// hashArgon2 derives an argon2id hash of secret using p. The params are embedded in the
// output ("v1$memory$time$threads$salt$key"), so verifyArgon2 works regardless of later
// param changes — lowering/raising cost never breaks existing hashes.
func hashArgon2(secret string, params Argon2Params) (string, error) {
	params = params.withDefaults()

	salt := make([]byte, argon2SaltLen)

	_, err := rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	key := argon2.IDKey([]byte(secret), salt, params.Time, params.Memory, params.Threads, argon2KeyLen)
	enc := base64.RawURLEncoding

	return fmt.Sprintf("v1$%d$%d$%d$%s$%s",
		params.Memory, params.Time, uint32(params.Threads),
		enc.EncodeToString(salt),
		enc.EncodeToString(key),
	), nil
}

// verifyArgon2 reports whether secret matches storedHash, reading the cost parameters from
// the stored hash. The final key comparison is constant-time.
//
// The early returns (bad format/version/base64) are not a timing oracle: storedHash is always
// a value we produced via hashArgon2 (well-formed), and the attacker-controlled input is
// secret, not storedHash — so those branches are unreachable from untrusted input. For the
// "user does not exist" case the auth layer verifies against a well-formed dummy hash, so that
// path runs the full argon2 work too.
func verifyArgon2(storedHash, secret string) bool {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 6 || parts[0] != "v1" {
		return false
	}

	memory, err := parseU32(parts[1])
	if err != nil {
		return false
	}

	time, err := parseU32(parts[2])
	if err != nil {
		return false
	}

	threads, err := parseU32(parts[3])
	if err != nil || threads > math.MaxUint8 {
		return false
	}

	enc := base64.RawURLEncoding

	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}

	expected, err := enc.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}

	//nolint:gosec // argon2 key length fits uint32 by design
	actual := argon2.IDKey([]byte(secret), salt, time, memory, uint8(threads), uint32(len(expected)))

	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func parseU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse u32: %w", err)
	}

	return uint32(v), nil
}
