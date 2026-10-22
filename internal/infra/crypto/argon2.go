package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
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

// Hard limits apply to stored costs as well as configuration, before allocating
// any Argon2 workspace. Reject, rather than clamp: changing a cost changes the key.
const (
	MaxArgon2Memory  uint32 = 256 * 1024 // KiB, 256 MiB per derivation
	MaxArgon2Time    uint32 = 10
	MaxArgon2Threads uint8  = 16
	maxArgon2HashLen        = 256
)

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

// Validate checks an explicit cost; callers may apply defaults to configuration first.
func (p Argon2Params) Validate() error {
	if p.Threads == 0 || p.Threads > MaxArgon2Threads || p.Time == 0 || p.Time > MaxArgon2Time ||
		p.Memory < 8*uint32(p.Threads) || p.Memory > MaxArgon2Memory {
		return errors.New("argon2 cost outside supported bounds " +
			"(memory: 8*threads..262144 KiB, time: 1..10, threads: 1..16)")
	}

	return nil
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
// supported param changes — cost changes within the hard limits preserve existing hashes.
func hashArgon2(secret string, params Argon2Params) (string, error) {
	params = params.withDefaults()

	err := params.Validate()
	if err != nil {
		return "", err
	}

	salt := make([]byte, argon2SaltLen)

	_, err = rand.Read(salt)
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
// Stored hashes may be corrupt or tampered with. Bound their size, parameters and
// output length before deriving. Missing users use a valid dummy hash in auth.
func verifyArgon2(storedHash, secret string) bool {
	if len(storedHash) > maxArgon2HashLen {
		return false
	}

	parts := strings.Split(storedHash, "$")
	if len(parts) != 6 || parts[0] != "v1" {
		return false
	}

	params, err := parseArgon2Cost(parts[1], parts[2], parts[3])
	if err != nil {
		return false
	}

	enc := base64.RawURLEncoding

	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) != argon2SaltLen {
		return false
	}

	expected, err := enc.DecodeString(parts[5])
	if err != nil || len(expected) != int(argon2KeyLen) {
		return false
	}

	actual := argon2.IDKey([]byte(secret), salt, params.Time, params.Memory, params.Threads, argon2KeyLen)

	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func parseU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse u32: %w", err)
	}

	return uint32(v), nil
}

// parseArgon2Cost parses untrusted stored parameters without applying config defaults.
func parseArgon2Cost(memoryText, timeText, threadsText string) (Argon2Params, error) {
	var none Argon2Params

	memory, err := parseU32(memoryText)
	if err != nil {
		return none, err
	}

	iterations, err := parseU32(timeText)
	if err != nil {
		return none, err
	}

	threads, err := parseU32(threadsText)
	if err != nil || threads > math.MaxUint8 {
		return none, errors.New("invalid argon2 threads")
	}

	params := Argon2Params{Memory: memory, Time: iterations, Threads: uint8(threads)}

	err = params.Validate()
	if err != nil {
		return none, err
	}

	return params, nil
}
