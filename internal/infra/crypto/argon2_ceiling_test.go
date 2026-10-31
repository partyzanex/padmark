package crypto

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hugeCostHash is a well-shaped "v1$" hash whose stored cost is within the hard caps (256 MiB,
// t=10) but far above any sane ceiling; deriving it would take seconds and a quarter-gigabyte.
func hugeCostHash() string {
	enc := base64.RawURLEncoding

	return fmt.Sprintf("v1$%d$%d$1$%s$%s", MaxArgon2Memory, MaxArgon2Time,
		enc.EncodeToString(make([]byte, argon2SaltLen)), enc.EncodeToString(make([]byte, argon2KeyLen)))
}

func TestAcceptedCeiling(t *testing.T) {
	t.Parallel()

	def := DefaultArgon2Params()

	cases := []struct {
		name string
		cfg  Argon2Params
		want Argon2Params
	}{
		{
			"low config is lifted to twice the default",
			Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1},
			Argon2Params{Memory: def.Memory * 2, Time: def.Time * 2, Threads: 2},
		},
		{
			"higher config doubles itself",
			Argon2Params{Memory: 96 * 1024, Time: 3, Threads: 2},
			Argon2Params{Memory: 192 * 1024, Time: 6, Threads: 4},
		},
		{
			"ceiling never exceeds the hard caps",
			Argon2Params{Memory: MaxArgon2Memory, Time: MaxArgon2Time, Threads: MaxArgon2Threads},
			Argon2Params{Memory: MaxArgon2Memory, Time: MaxArgon2Time, Threads: MaxArgon2Threads},
		},
	}

	for _, testCase := range cases {
		assert.Equal(t, testCase.want, testCase.cfg.acceptedCeiling(), testCase.name)
	}
}

func TestVerifyArgon2_RejectsCostAboveCeilingWithoutDeriving(t *testing.T) {
	t.Parallel()

	ceiling := Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1}.acceptedCeiling()

	start := time.Now()
	matched, err := verifyArgon2(hugeCostHash(), "password", ceiling)

	require.ErrorIs(t, err, ErrArgon2CostTooHigh)
	assert.False(t, matched)
	assert.Less(t, time.Since(start), 250*time.Millisecond, "must reject before running the derivation")
}

func TestVerifyArgon2_AcceptsConfiguredAndLegacyDefaultCost(t *testing.T) {
	t.Parallel()

	low := Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1}
	ceiling := low.acceptedCeiling()

	configured, err := hashArgon2("secret", low)
	require.NoError(t, err)

	matched, err := verifyArgon2(configured, "secret", ceiling)
	require.NoError(t, err)
	assert.True(t, matched, "hash at the configured cost verifies")

	legacy, err := hashArgon2("secret", DefaultArgon2Params())
	require.NoError(t, err)

	matched, err = verifyArgon2(legacy, "secret", ceiling)
	require.NoError(t, err)
	assert.True(t, matched, "a default-cost hash still verifies when the configured cost is lower")
}

func TestHashers_LogAndRejectCostAboveCeiling(t *testing.T) {
	t.Parallel()

	low := Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1}

	var buf bytes.Buffer

	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level:     slog.LevelWarn,
		AddSource: false, ReplaceAttr: nil,
	}))

	assert.False(t, NewPasswordHasher(low).WithLogger(log).Verify(hugeCostHash(), "password"))
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "password")

	buf.Reset()

	assert.False(t, NewEditCodeHasher(low).WithLogger(log).Verify(hugeCostHash(), "code"))
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "edit code")
	assert.NotContains(t, buf.String(), hugeCostHash(), "the stored hash must never be logged")
}

func TestSealer_OpenRejectsCostAboveCeiling(t *testing.T) {
	t.Parallel()

	sealer := NewSealer(Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1})
	envelope := fmt.Sprintf("v1$%d$%d$1$AAAA", MaxArgon2Memory, MaxArgon2Time)

	start := time.Now()
	_, _, err := sealer.Open(envelope, []byte("password"), make([]byte, pwKDFLen))

	require.ErrorIs(t, err, ErrArgon2CostTooHigh)
	assert.Less(t, time.Since(start), 250*time.Millisecond)
}
