package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// sweepWait limits each wait for the loop goroutine. The loop does not sleep, so this value is
// only a guard against a test that does not end.
const sweepWait = 5 * time.Second

type SweepLoopSuite struct {
	suite.Suite

	ticks   chan time.Time
	results chan error
	calls   chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
}

func TestSweepLoopSuite(t *testing.T) {
	suite.Run(t, new(SweepLoopSuite))
}

func (s *SweepLoopSuite) SetupTest() {
	s.ticks = make(chan time.Time)
	s.results = make(chan error, 2)
	s.calls = make(chan struct{}, 2)
	s.done = make(chan struct{})

	ctx, cancel := context.WithCancel(s.T().Context())
	s.cancel = cancel

	sweep := func(context.Context) (int, error) {
		s.calls <- struct{}{}

		return 1, <-s.results
	}

	go func() {
		defer close(s.done)

		runSweepLoop(ctx, s.ticks, sweep, slog.New(slog.DiscardHandler))
	}()
}

func (s *SweepLoopSuite) TearDownTest() {
	s.cancel()
	s.waitDone()
}

func (s *SweepLoopSuite) TestSweepsAtEachTick() {
	s.results <- nil

	s.results <- nil

	s.tick()
	s.waitCall()
	s.tick()
	s.waitCall()
}

func (s *SweepLoopSuite) TestContinuesAfterSweepError() {
	s.results <- errors.New("database is locked")

	s.results <- nil

	s.tick()
	s.waitCall()
	s.tick()
	s.waitCall()
}

func (s *SweepLoopSuite) TestStopsWhenContextIsCanceled() {
	s.cancel()
	s.waitDone()

	select {
	case s.ticks <- time.Now():
		s.Fail("the loop received a tick after it stopped")
	default:
	}

	s.Empty(s.calls, "no sweep without a tick")
}

// --- helpers ---

// tick sends one tick. The channel has no buffer, so the send ends when the loop receives it.
func (s *SweepLoopSuite) tick() {
	select {
	case s.ticks <- time.Now():
	case <-time.After(sweepWait):
		s.FailNow("the loop did not receive the tick")
	}
}

func (s *SweepLoopSuite) waitCall() {
	select {
	case <-s.calls:
	case <-time.After(sweepWait):
		s.FailNow("the loop did not call sweep")
	}
}

func (s *SweepLoopSuite) waitDone() {
	select {
	case <-s.done:
	case <-time.After(sweepWait):
		s.FailNow("the loop did not stop")
	}
}
