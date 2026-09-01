package crypto_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// sealTestParams keeps the argon2 cost low so these tests stay fast; the cost itself is not
// what they exercise.
//
//nolint:gochecknoglobals // test fixture
var sealTestParams = crypto.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1}

// sealTestSecret is a well-known base32 TOTP test vector, not a real credential.
//
//nolint:gosec // G101: test fixture
const sealTestSecret = "JBSWY3DPEHPK3PXP"

const sealTestPassword = "correct horse battery staple"

//nolint:gochecknoglobals // test fixture
var sealTestSalt = []byte("saltsaltsalt1234")

// legacyHKDFSecret reproduces the pre-argon2id sealing scheme (HKDF-SHA256 → hex key → AES-GCM)
// from outside the package, so the compatibility path is pinned against an independent
// implementation rather than against the production code's own helper.
func legacyHKDFSecret(t *testing.T, secret, password string, salt []byte) string {
	t.Helper()

	reader := hkdf.New(sha256.New, []byte(password), salt, []byte("padmark-user-key-v1"))

	key := make([]byte, 32)
	_, err := io.ReadFull(reader, key)
	require.NoError(t, err)

	ciphertext, err := crypto.New().Encrypt(secret, hex.EncodeToString(key))
	require.NoError(t, err)

	return ciphertext
}

// legacyArgon2Secret reproduces the intermediate scheme: argon2id-derived key, but a bare
// ciphertext with no envelope recording the cost it was derived at.
func legacyArgon2Secret(t *testing.T, secret, password string, salt []byte, params crypto.Argon2Params) string {
	t.Helper()

	key, err := crypto.DeriveUserKey([]byte(password), salt, params)
	require.NoError(t, err)

	ciphertext, err := crypto.New().Encrypt(secret, key)
	require.NoError(t, err)

	return ciphertext
}

func TestSealer_RoundTrip(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	sealed, err := sealer.Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(sealed, "v1$8192$1$1$"), "envelope records its argon2 cost: %s", sealed)

	got, stale, err := sealer.Open(sealed, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.Equal(t, sealTestSecret, got)
	require.False(t, stale, "freshly sealed at the current cost is not stale")
}

func TestSealer_SealIsNondeterministic(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	first, err := sealer.Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	second, err := sealer.Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	require.NotEqual(t, first, second, "each seal must use a fresh GCM nonce")
}

// TestSealer_OpenSurvivesCostChange is the regression guard for the lockout this envelope
// exists to prevent: an operator raising --argon2-memory must not orphan stored secrets.
func TestSealer_OpenSurvivesCostChange(t *testing.T) {
	sealed, err := crypto.NewSealer(sealTestParams).
		Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	raised := crypto.Argon2Params{Memory: 16 * 1024, Time: 2, Threads: 1}

	got, stale, err := crypto.NewSealer(raised).Open(sealed, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err, "a cost change must not make stored secrets unreadable")
	require.Equal(t, sealTestSecret, got)
	require.True(t, stale, "sealed under an outdated cost → caller must re-seal")
}

func TestSealer_ResealAtNewCostClearsStale(t *testing.T) {
	sealed, err := crypto.NewSealer(sealTestParams).
		Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	raised := crypto.Argon2Params{Memory: 16 * 1024, Time: 2, Threads: 1}
	sealer := crypto.NewSealer(raised)

	got, _, err := sealer.Open(sealed, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	resealed, err := sealer.Seal(got, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(resealed, "v1$16384$2$1$"), "re-seal records the new cost: %s", resealed)

	got, stale, err := sealer.Open(resealed, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.Equal(t, sealTestSecret, got)
	require.False(t, stale, "re-sealed at the current cost → migration complete")
}

// TestSealer_OpenLegacyHKDF covers accounts created before the switch to argon2id: their
// secrets must still open, and must report stale so login migrates them.
func TestSealer_OpenLegacyHKDF(t *testing.T) {
	legacy := legacyHKDFSecret(t, sealTestSecret, sealTestPassword, sealTestSalt)

	got, stale, err := crypto.NewSealer(sealTestParams).
		Open(legacy, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err, "pre-argon2id secrets must remain readable")
	require.Equal(t, sealTestSecret, got)
	require.True(t, stale)
}

// TestSealer_OpenLegacyArgon2 covers accounts created after the argon2id switch but before the
// envelope carried its parameters.
func TestSealer_OpenLegacyArgon2(t *testing.T) {
	legacy := legacyArgon2Secret(t, sealTestSecret, sealTestPassword, sealTestSalt, sealTestParams)

	got, stale, err := crypto.NewSealer(sealTestParams).
		Open(legacy, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.Equal(t, sealTestSecret, got)
	require.True(t, stale)
}

// TestSealer_MigrateLegacyEndToEnd walks the full migration a login performs: open the old
// value, re-seal it, and confirm the result is a current, no-longer-stale envelope.
func TestSealer_MigrateLegacyEndToEnd(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)
	legacy := legacyHKDFSecret(t, sealTestSecret, sealTestPassword, sealTestSalt)

	opened, stale, err := sealer.Open(legacy, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.True(t, stale)

	migrated, err := sealer.Seal(opened, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	got, stale, err := sealer.Open(migrated, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)
	require.Equal(t, sealTestSecret, got)
	require.False(t, stale)
}

// TestSealer_LegacyArgon2AtOtherCostFails documents the one case the envelope cannot rescue:
// a bare ciphertext carries no parameters, so if the configured cost changed before that value
// was ever migrated, it is unreadable. This is exactly the failure mode versioning removes for
// everything sealed from now on.
func TestSealer_LegacyArgon2AtOtherCostFails(t *testing.T) {
	legacy := legacyArgon2Secret(t, sealTestSecret, sealTestPassword, sealTestSalt, sealTestParams)

	other := crypto.Argon2Params{Memory: 16 * 1024, Time: 2, Threads: 1}

	_, _, err := crypto.NewSealer(other).Open(legacy, []byte(sealTestPassword), sealTestSalt)
	require.Error(t, err)
}

func TestSealer_OpenWrongPassword(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	sealed, err := sealer.Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	_, _, err = sealer.Open(sealed, []byte("wrong password entirely"), sealTestSalt)
	require.Error(t, err)
}

func TestSealer_OpenWrongSalt(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	sealed, err := sealer.Seal(sealTestSecret, []byte(sealTestPassword), sealTestSalt)
	require.NoError(t, err)

	_, _, err = sealer.Open(sealed, []byte(sealTestPassword), []byte("differentsalt456"))
	require.Error(t, err)
}

func TestSealer_OpenMalformed(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	cases := map[string]string{
		"empty":                "",
		"not base64":           "!!!not-base64!!!",
		"unknown version":      "v9$8192$1$1$AAAA",
		"non-numeric cost":     "v1$lots$1$1$AAAA",
		"threads out of range": "v1$8192$1$999$AAAA",
		"too few fields":       "v1$8192$1$AAAA",
	}

	for name, sealed := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := sealer.Open(sealed, []byte(sealTestPassword), sealTestSalt)
			require.Error(t, err)
		})
	}
}

func TestSealer_GenerateSalt(t *testing.T) {
	sealer := crypto.NewSealer(sealTestParams)

	first, err := sealer.GenerateSalt()
	require.NoError(t, err)
	require.Len(t, first, 16)

	second, err := sealer.GenerateSalt()
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}
