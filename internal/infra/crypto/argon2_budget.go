package crypto

import (
	"context"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
	"golang.org/x/sync/semaphore"
)

// DefaultArgon2MemoryBudget is the process-wide argon2id workspace budget in KiB used until
// SetArgon2MemoryBudget is called: 128 MiB, room for two 64 MiB derivations (caller-chosen
// edit codes) or five 24 MiB ones (the default password cost).
const DefaultArgon2MemoryBudget uint32 = 128 * 1024

// argon2Budget bounds the argon2id workspace held by concurrent derivations. argon2.IDKey
// allocates params.Memory 1 KiB blocks per call, so the weight of a derivation is exactly its
// memory cost in KiB. semaphore.Weighted grants waiters in FIFO order, so a stream of small
// derivations cannot starve a large one.
type argon2Budget struct {
	sem      *semaphore.Weighted
	capacity int64
}

// newArgon2Budget returns a budget of kib KiB; zero selects DefaultArgon2MemoryBudget.
func newArgon2Budget(kib uint32) *argon2Budget {
	if kib == 0 {
		kib = DefaultArgon2MemoryBudget
	}

	return &argon2Budget{sem: semaphore.NewWeighted(int64(kib)), capacity: int64(kib)}
}

// weight clamps a derivation larger than the whole budget to the budget, so it waits for
// exclusive access instead of blocking forever.
func (b *argon2Budget) weight(kib uint32) int64 {
	return min(int64(kib), b.capacity)
}

// acquire blocks until kib KiB of the budget are free and returns the matching release.
func (b *argon2Budget) acquire(kib uint32) (func(), error) {
	weight := b.weight(kib)

	// The hasher API (Hash, Verify, DeriveUserKey) carries no context and is consumed across
	// the usecases, so the wait cannot be cancelled here; the HTTP server timeouts bound how
	// long a request may queue. With a never-cancelled context and weight clamped to the
	// semaphore size, Acquire cannot fail; the error is still propagated, never ignored.
	err := b.sem.Acquire(context.Background(), weight)
	if err != nil {
		return nil, fmt.Errorf("acquire argon2 memory budget: %w", err)
	}

	return func() { b.sem.Release(weight) }, nil
}

// tryAcquire is the non-blocking form of acquire; ok is false when kib KiB are not free now
// or earlier waiters are queued.
func (b *argon2Budget) tryAcquire(kib uint32) (func(), bool) {
	weight := b.weight(kib)

	if !b.sem.TryAcquire(weight) {
		return nil, false
	}

	return func() { b.sem.Release(weight) }, true
}

// processArgon2Budget is the single budget shared by every argon2id derivation in the
// process. It is package-level rather than injected because memory is a process-wide
// resource and the hasher constructors and method signatures are consumed across the
// usecases; SetArgon2MemoryBudget configures it once at startup.
var processArgon2Budget atomic.Pointer[argon2Budget] //nolint:gochecknoglobals // process-wide memory bound

// SetArgon2MemoryBudget caps the total argon2id workspace (in KiB) that concurrent password,
// edit-code and user-key derivations may hold at once; further derivations wait in FIFO order.
// Zero restores DefaultArgon2MemoryBudget. A single derivation costing more than the budget
// still runs, alone. Call it at startup before serving: derivations already waiting keep the
// budget they started on.
func SetArgon2MemoryBudget(kib uint32) {
	processArgon2Budget.Store(newArgon2Budget(kib))
}

// Argon2MemoryBudget returns the current process-wide argon2id workspace budget in KiB.
func Argon2MemoryBudget() uint32 {
	return uint32(currentArgon2Budget().capacity) //nolint:gosec // G115: capacity is built from a uint32
}

func currentArgon2Budget() *argon2Budget {
	budget := processArgon2Budget.Load()
	if budget != nil {
		return budget
	}

	processArgon2Budget.CompareAndSwap(nil, newArgon2Budget(DefaultArgon2MemoryBudget))

	return processArgon2Budget.Load()
}

// deriveArgon2Key is the only caller of argon2.IDKey: it holds params.Memory KiB of the
// process budget for the duration of the derivation.
func deriveArgon2Key(secret, salt []byte, params Argon2Params, keyLen uint32) ([]byte, error) {
	release, err := currentArgon2Budget().acquire(params.Memory)
	if err != nil {
		return nil, err
	}

	defer release()

	return argon2.IDKey(secret, salt, params.Time, params.Memory, params.Threads, keyLen), nil
}
