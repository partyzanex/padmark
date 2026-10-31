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
)

const argon2KeyLen uint32 = 32

const argon2SaltLen = 16

// argon2MinMemoryPerLaneKiB is the smallest memory, in KiB, that argon2id accepts per thread.
const argon2MinMemoryPerLaneKiB uint32 = 8

// Indexes of the "$"-separated fields in an argon2id hash: version, memory, time, threads,
// salt and key. argon2FieldCount is the total number of fields.
const (
	argon2FieldMemory  = 1
	argon2FieldTime    = 2
	argon2FieldThreads = 3
	argon2FieldSalt    = 4
	argon2FieldKey     = 5
	argon2FieldCount   = 6
)

// decimalBase is the radix for the decimal numbers stored in an argon2id hash.
const decimalBase = 10

// uint32BitSize is the bit width that bounds the stored cost fields.
const uint32BitSize = 32

// defaultArgon2MemoryKiB is the built-in argon2id memory cost (64 MiB).
const defaultArgon2MemoryKiB uint32 = 64 * 1024

// defaultArgon2Time is the built-in argon2id iteration count (OWASP minimum at 64 MiB).
const defaultArgon2Time uint32 = 2

// Hard limits apply to stored costs as well as configuration, before allocating
// any Argon2 workspace. Reject, rather than clamp: changing a cost changes the key.
const (
	MaxArgon2Memory  uint32 = 256 * 1024 // KiB, 256 MiB per derivation.
	MaxArgon2Time    uint32 = 10
	MaxArgon2Threads uint8  = 16
	maxArgon2HashLen        = 256
)

// ErrArgon2CostTooHigh reports a stored argon2id cost above the accepted ceiling (see
// Argon2Params.acceptedCeiling). The derivation is not run: the hard caps alone still allow a
// 256 MiB, 10-pass derivation per attempt, which a tampered or corrupt row could turn into a
// memory/CPU amplifier on every login.
var ErrArgon2CostTooHigh = errors.New("stored argon2 cost exceeds the accepted ceiling")

// argon2CeilingFactor is how far above max(configured, built-in default) a stored cost may sit
// before it is refused. Two leaves room for operators to have raised the cost once or twice and
// for threads/time to have been traded against memory, without letting a row demand up to the
// hard caps (4x the default memory, 5x the default passes).
const argon2CeilingFactor = 2

// Argon2Params holds tunable argon2id cost parameters shared by password and edit-code
// hashing. Memory is in KiB; raising memory or time increases brute-force cost (and CPU/RAM
// per hash). Cost depends on these params, not on input length.
type Argon2Params struct {
	Memory  uint32 // KiB (e.g. 65536 = 64 MiB).
	Time    uint32 // iterations.
	Threads uint8  // parallelism (CPU cores).
}

// DefaultArgon2Params returns the built-in defaults (64 MiB, time=2, threads=1).
// time=2 follows the OWASP argon2id minimum at this memory cost.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: defaultArgon2MemoryKiB, Time: defaultArgon2Time, Threads: 1}
}

// Validate checks an explicit cost; callers may apply defaults to configuration first.
func (p Argon2Params) Validate() error {
	if p.Threads == 0 || p.Threads > MaxArgon2Threads || p.Time == 0 || p.Time > MaxArgon2Time ||
		p.Memory < argon2MinMemoryPerLaneKiB*uint32(p.Threads) || p.Memory > MaxArgon2Memory {
		return errInvalidArgon2Cost
	}

	return nil
}

// acceptedCeiling returns the highest stored cost a verifier or sealer will run: per field,
// argon2CeilingFactor times the larger of the configured value and the built-in default,
// clamped to the hard caps. The default is included so hashes written by an earlier, stronger
// configuration (or by the built-in defaults) still verify after the operator lowers the cost.
func (p Argon2Params) acceptedCeiling() Argon2Params {
	cfg, def := p.withDefaults(), DefaultArgon2Params()

	return Argon2Params{
		Memory:  min(max(cfg.Memory, def.Memory)*argon2CeilingFactor, MaxArgon2Memory),
		Time:    min(max(cfg.Time, def.Time)*argon2CeilingFactor, MaxArgon2Time),
		Threads: min(max(cfg.Threads, def.Threads)*argon2CeilingFactor, MaxArgon2Threads),
	}
}

// exceeds reports whether any field of p is above the matching field of ceiling.
func (p Argon2Params) exceeds(ceiling Argon2Params) bool {
	return p.Memory > ceiling.Memory || p.Time > ceiling.Time || p.Threads > ceiling.Threads
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

// hashArgon2 derives an argon2id hash of secret using requested, with defaults for zero fields.
// The params are embedded in the output ("v1$memory$time$threads$salt$key"), so verifyArgon2
// works regardless of later supported param changes — cost changes within the hard limits preserve existing hashes.
func hashArgon2(secret string, requested Argon2Params) (string, error) {
	params := requested.withDefaults()

	err := params.Validate()
	if err != nil {
		return "", fmt.Errorf("validate: %w", err)
	}

	salt := make([]byte, argon2SaltLen)

	_, err = rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	key, err := deriveArgon2Key([]byte(secret), salt, params, argon2KeyLen)
	if err != nil {
		return "", fmt.Errorf("derive argon2 key: %w", err)
	}

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
// Stored hashes may be corrupt or tampered with. Bound their size, parameters and output length
// before deriving. A cost inside the hard caps but above ceiling is refused with
// ErrArgon2CostTooHigh (and false) without running the derivation; every other malformed input
// is a plain (false, nil). Missing users use a valid dummy hash in auth.
func verifyArgon2(storedHash, secret string, ceiling Argon2Params) (bool, error) {
	if len(storedHash) > maxArgon2HashLen {
		return false, nil
	}

	parts := strings.Split(storedHash, "$")
	if len(parts) != argon2FieldCount || parts[0] != "v1" {
		return false, nil
	}

	params, err := parseArgon2Cost(parts[argon2FieldMemory], parts[argon2FieldTime], parts[argon2FieldThreads])
	if err != nil {
		return false, nil //nolint:nilerr // a malformed stored cost is a failed match, not an operational error
	}

	if params.exceeds(ceiling) {
		return false, ErrArgon2CostTooHigh
	}

	enc := base64.RawURLEncoding

	salt, err := enc.DecodeString(parts[argon2FieldSalt])
	if err != nil || len(salt) != argon2SaltLen {
		return false, nil //nolint:nilerr // see above
	}

	expected, err := enc.DecodeString(parts[argon2FieldKey])
	if err != nil || len(expected) != int(argon2KeyLen) {
		return false, nil //nolint:nilerr // see above
	}

	actual, err := deriveArgon2Key([]byte(secret), salt, params, argon2KeyLen)
	if err != nil {
		return false, fmt.Errorf("derive argon2 key: %w", err)
	}

	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

func parseU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, decimalBase, uint32BitSize)
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
		return none, fmt.Errorf("parse u32: %w", err)
	}

	iterations, err := parseU32(timeText)
	if err != nil {
		return none, fmt.Errorf("parse u32: %w", err)
	}

	threads, err := parseU32(threadsText)
	if err != nil || threads > math.MaxUint8 {
		return none, errInvalidArgon2Threads
	}

	params := Argon2Params{Memory: memory, Time: iterations, Threads: uint8(threads)}

	err = params.Validate()
	if err != nil {
		return none, fmt.Errorf("validate: %w", err)
	}

	return params, nil
}
