package crypto_test

import (
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

const (
	memBudgetKiB   = 64 * 1024
	memPerHashKiB  = 32 * 1024
	memConcurrency = 8
	memSampleEvery = time.Millisecond
	kib            = 1024
	mib            = 1024 * kib
)

// Argon2MemoryBoundSuite proves that concurrent argon2id derivations cannot hold more
// workspace than the process-wide budget: without the bound, a burst of anonymous edit-code
// creates or logins allocates params.Memory per request at once and OOM-kills a small container.
type Argon2MemoryBoundSuite struct {
	suite.Suite
}

func TestArgon2MemoryBoundSuite(t *testing.T) {
	suite.Run(t, new(Argon2MemoryBoundSuite))
}

func (s *Argon2MemoryBoundSuite) TestConcurrentDerivationsStayWithinBudget() {
	if testing.Short() {
		s.T().Skip("allocates several 32 MiB argon2 workspaces")
	}

	previousBudget := crypto.Argon2MemoryBudget()
	defer crypto.SetArgon2MemoryBudget(previousBudget)

	crypto.SetArgon2MemoryBudget(memBudgetKiB)

	runtime.GC()

	var stats runtime.MemStats

	runtime.ReadMemStats(&stats)
	baseline := stats.HeapInuse

	// Keep unreferenced (already finished) argon2 workspaces from inflating the reading: a soft
	// limit below the budget makes the GC collect before (almost) every 32 MiB allocation, so
	// the samples track live workspaces plus at most the one being allocated. A looser limit
	// (baseline + budget + 32 MiB) lets two dead workspaces pile up before the GC catches up,
	// which put a correctly bounded run right at the 2x threshold.
	previousLimit := debug.SetMemoryLimit(int64(baseline) + memBudgetKiB*kib/2) //nolint:gosec // G115: heap size << 2^63
	defer debug.SetMemoryLimit(previousLimit)

	var peak atomic.Uint64

	stop := make(chan struct{})

	var sampler sync.WaitGroup

	sampler.Go(func() { sampleHeapInuse(stop, &peak) })

	hasher := crypto.NewPasswordHasher(crypto.Argon2Params{Memory: memPerHashKiB, Time: 1, Threads: 1})

	var workers sync.WaitGroup

	errs := make(chan error, memConcurrency)

	for range memConcurrency {
		workers.Go(func() {
			_, err := hasher.Hash("correct horse battery staple")
			errs <- err
		})
	}

	workers.Wait()
	close(stop)
	sampler.Wait()
	close(errs)

	for err := range errs {
		s.Require().NoError(err)
	}

	var growth uint64
	if top := peak.Load(); top > baseline {
		growth = top - baseline
	}

	limit := uint64(2 * memBudgetKiB * kib)

	s.T().Logf("peak heap growth: %d MiB (budget %d MiB)", growth/mib, memBudgetKiB/kib)

	s.LessOrEqualf(growth, limit,
		"peak heap growth %d MiB exceeds 2x the %d MiB argon2 budget (%d x %d MiB derivations ran unbounded)",
		growth/mib, memBudgetKiB/kib, memConcurrency, memPerHashKiB/kib)
}

// sampleHeapInuse records the maximum HeapInuse observed until stop is closed.
func sampleHeapInuse(stop <-chan struct{}, peak *atomic.Uint64) {
	ticker := time.NewTicker(memSampleEvery)
	defer ticker.Stop()

	var stats runtime.MemStats

	for {
		runtime.ReadMemStats(&stats)

		if stats.HeapInuse > peak.Load() {
			peak.Store(stats.HeapInuse)
		}

		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
