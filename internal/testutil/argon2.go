package testutil

import "github.com/partyzanex/padmark/internal/infra/crypto"

// Argon2Params returns the cheapest argon2id cost that the hashers accept: 8 KiB, one pass,
// one thread. Integration tests check the behavior around hashing, not its strength; with the
// production cost (crypto.DefaultArgon2Params, 64 MiB and two passes) the race detector makes
// each hash take seconds. The tests of internal/infra/crypto keep the production cost.
func Argon2Params() crypto.Argon2Params {
	const minMemoryKiB = 8 // argon2id needs at least 8 KiB for each thread.

	return crypto.Argon2Params{Memory: minMemoryKiB, Time: 1, Threads: 1}
}
