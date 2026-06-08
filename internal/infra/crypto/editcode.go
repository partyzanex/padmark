package crypto

// EditCodeHasher hashes and verifies note edit codes using argon2id with configurable cost.
type EditCodeHasher struct {
	params Argon2Params
}

// NewEditCodeHasher returns an EditCodeHasher using the given argon2 cost parameters.
// Zero-valued fields fall back to DefaultArgon2Params.
func NewEditCodeHasher(params Argon2Params) *EditCodeHasher {
	return &EditCodeHasher{params: params.withDefaults()}
}

// Hash derives an argon2id hash from code.
// Stored format: "v1$<memory>$<time>$<threads>$<base64url_salt>$<base64url_key>" —
// parameters are embedded so future cost changes don't break existing hashes.
func (h *EditCodeHasher) Hash(code string) (string, error) {
	return hashArgon2(code, h.params)
}

// Verify reports whether code matches storedHash.
// Reads parameters from the stored hash; uses constant-time comparison.
func (h *EditCodeHasher) Verify(storedHash, code string) bool {
	return verifyArgon2(storedHash, code)
}
