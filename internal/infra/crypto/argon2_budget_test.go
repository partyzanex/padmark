package crypto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	budgetWaitFor = 2 * time.Second
	budgetTick    = 5 * time.Millisecond
	// budgetStillBlocked is how long a waiter must stay blocked. A broken budget lets it
	// through immediately, so a short window cannot produce a false failure, only a miss.
	budgetStillBlocked = 50 * time.Millisecond
)

type Argon2BudgetSuite struct {
	suite.Suite
}

func TestArgon2BudgetSuite(t *testing.T) {
	suite.Run(t, new(Argon2BudgetSuite))
}

// acquireAsync starts a blocking acquire and returns a channel that delivers its release
// function once the weight has been granted.
func acquireAsync(budget *argon2Budget, kib uint32) <-chan func() {
	granted := make(chan func(), 1)

	go func() {
		release, err := budget.acquire(kib)
		if err == nil {
			granted <- release
		}
	}()

	return granted
}

// awaitGrant waits for an acquireAsync grant, failing instead of hanging if it never comes.
func (s *Argon2BudgetSuite) awaitGrant(granted <-chan func(), msg string) {
	select {
	case release := <-granted:
		release()
	case <-time.After(budgetWaitFor):
		s.Fail(msg)
	}
}

func (s *Argon2BudgetSuite) TestZeroCapacityFallsBackToDefault() {
	s.Equal(int64(DefaultArgon2MemoryBudget), newArgon2Budget(0).capacity)
}

func (s *Argon2BudgetSuite) TestAcquireWithinCapacityDoesNotBlock() {
	budget := newArgon2Budget(4096)

	first, ok := budget.tryAcquire(2048)
	s.Require().True(ok, "first half of the budget must be granted immediately")

	second, ok := budget.tryAcquire(2048)
	s.Require().True(ok, "second half of the budget must be granted immediately")

	second()
	first()
}

func (s *Argon2BudgetSuite) TestAcquireBeyondCapacityBlocksUntilRelease() {
	budget := newArgon2Budget(4096)

	held, ok := budget.tryAcquire(3072)
	s.Require().True(ok)

	_, ok = budget.tryAcquire(2048)
	s.Require().False(ok, "3072 + 2048 KiB exceeds the 4096 KiB budget")

	granted := acquireAsync(budget, 2048)

	s.Never(func() bool { return len(granted) > 0 }, budgetStillBlocked, budgetTick,
		"an acquire exceeding the remaining budget must wait")

	held()

	s.awaitGrant(granted, "waiter was not granted after the holder released")
}

func (s *Argon2BudgetSuite) TestWeightAboveCapacityIsClampedAndExclusive() {
	budget := newArgon2Budget(1024)

	// A single derivation larger than the whole budget must still run, alone.
	var release func()

	select {
	case release = <-acquireAsync(budget, 64*1024):
	case <-time.After(budgetWaitFor):
		s.FailNow("an oversized derivation must be clamped to the budget, not block forever")
	}

	_, granted := budget.tryAcquire(1)
	s.Require().False(granted, "an oversized derivation must hold the whole budget exclusively")

	release()

	again, granted := budget.tryAcquire(1024)
	s.Require().True(granted, "release must return the whole (clamped) weight")
	again()
}

func (s *Argon2BudgetSuite) TestLargeWaiterIsNotStarvedBySmallAcquires() {
	budget := newArgon2Budget(4096)

	small, ok := budget.tryAcquire(1024)
	s.Require().True(ok)

	large := acquireAsync(budget, 4096)

	// Once the large waiter is queued, later small requests must queue behind it even though
	// 3072 KiB are free — otherwise a stream of small derivations starves the large one.
	s.Eventually(func() bool {
		release, granted := budget.tryAcquire(1024)
		if granted {
			release()
		}

		return !granted
	}, budgetWaitFor, budgetTick, "small acquires must not overtake a queued large waiter")

	small()

	s.awaitGrant(large, "large waiter was not granted after the budget drained")
}

func (s *Argon2BudgetSuite) TestSetArgon2MemoryBudgetReplacesProcessBudget() {
	previous := Argon2MemoryBudget()
	defer SetArgon2MemoryBudget(previous)

	SetArgon2MemoryBudget(32 * 1024)
	s.Equal(uint32(32*1024), Argon2MemoryBudget())

	SetArgon2MemoryBudget(0)
	s.Equal(DefaultArgon2MemoryBudget, Argon2MemoryBudget(), "zero restores the default")
}
