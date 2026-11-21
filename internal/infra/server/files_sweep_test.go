package server

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/stretchr/testify/suite"
)

// sweepWait limits each wait for the loop goroutine. The loop does not sleep, so this value is
// only a guard against a test that does not end.
const sweepWait = 5 * time.Second

// errSweepPanic makes the test sweep panic in place of a return.
var errSweepPanic = errors.New("test: panic in sweep")

type SweepLoopSuite struct {
	suite.Suite

	ticks   chan time.Time
	results chan error
	calls   chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	logs    *syncBuffer
}

func TestSweepLoopSuite(t *testing.T) {
	suite.Run(t, new(SweepLoopSuite))
}

func (s *SweepLoopSuite) SetupTest() {
	s.ticks = make(chan time.Time)
	s.results = make(chan error, 2)
	s.calls = make(chan struct{}, 2)
	s.done = make(chan struct{})
	s.logs = new(syncBuffer)

	ctx, cancel := context.WithCancel(s.T().Context())
	s.cancel = cancel

	sweep := func(context.Context) (int, error) {
		s.calls <- struct{}{}

		err := <-s.results
		if errors.Is(err, errSweepPanic) {
			panic(err.Error())
		}

		return 1, err
	}

	log := slog.New(slog.NewTextHandler(s.logs, nil))

	go func() {
		defer close(s.done)

		runSweepLoop(ctx, s.ticks, sweep, log)
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

func (s *SweepLoopSuite) TestRecoversFromPanic() {
	s.results <- errSweepPanic

	s.results <- nil

	s.tick()
	s.waitCall()
	s.tick()
	s.waitCall()

	s.Contains(s.logs.String(), "level=ERROR")
	s.Contains(s.logs.String(), "background file sweep panicked")
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

// StartSweepSuite checks startFileSweep: the sweep at startup and the close function.
type StartSweepSuite struct {
	suite.Suite

	background *shutdown.Lifo
	log        *slog.Logger
}

func TestStartSweepSuite(t *testing.T) {
	suite.Run(t, new(StartSweepSuite))
}

func (s *StartSweepSuite) SetupTest() {
	s.background = shutdown.NewLIFO()
	s.log = slog.New(slog.DiscardHandler)
}

func (s *StartSweepSuite) TearDownTest() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.T().Context()), sweepWait)
	defer cancel()

	_ = s.background.CloseContext(ctx)
}

func (s *StartSweepSuite) TestZeroIntervalStartsNothing() {
	called := make(chan struct{}, 1)

	startFileSweep(s.T().Context(), 0, func(context.Context) (int, error) {
		called <- struct{}{}

		return 0, nil
	}, s.background, s.log)

	s.Require().NoError(s.background.CloseContext(s.T().Context()))
	s.Empty(called, "an interval of 0 must not sweep")
}

func (s *StartSweepSuite) TestSweepsAtStartupWithoutTick() {
	called := make(chan struct{}, 1)

	startFileSweep(s.T().Context(), time.Hour, func(context.Context) (int, error) {
		called <- struct{}{}

		return 0, nil
	}, s.background, s.log)

	select {
	case <-called:
	case <-time.After(sweepWait):
		s.FailNow("no sweep at startup")
	}
}

func (s *StartSweepSuite) TestCloseWaitsForRunningSweep() {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	startFileSweep(s.T().Context(), time.Hour, func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		<-release
		close(finished)

		return 0, ctx.Err()
	}, s.background, s.log)

	s.waitClosed(started, "the sweep did not start")

	closed := make(chan error, 1)

	go func() { closed <- s.background.CloseContext(s.T().Context()) }()

	select {
	case <-closed:
		s.FailNow("the close function returned while the sweep was running")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-closed:
		s.Require().NoError(err)
	case <-time.After(sweepWait):
		s.FailNow("the close function did not return")
	}

	s.waitClosed(finished, "the sweep did not finish")
}

func (s *StartSweepSuite) TestCloseStopsWaitingAtDeadline() {
	started := make(chan struct{})
	release := make(chan struct{})

	startFileSweep(s.T().Context(), time.Hour, func(context.Context) (int, error) {
		close(started)
		<-release // The sweep ignores the cancel, as a slow storage call can.

		return 0, nil
	}, s.background, s.log)

	s.waitClosed(started, "the sweep did not start")

	ctx, cancel := context.WithTimeout(s.T().Context(), 50*time.Millisecond)
	defer cancel()

	err := s.background.CloseContext(ctx)

	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Contains(err.Error(), "stop file sweep")

	close(release)
}

func (s *StartSweepSuite) TestPanicAtStartupDoesNotStopTheLoop() {
	var count atomic.Int32

	second := make(chan struct{})

	startFileSweep(s.T().Context(), time.Millisecond, func(context.Context) (int, error) {
		switch count.Add(1) {
		case 1:
			panic("first sweep fails")
		case 2:
			close(second)
		}

		return 0, nil
	}, s.background, s.log)

	s.waitClosed(second, "the loop stopped after the panic")
}

// --- helpers ---

func (s *StartSweepSuite) waitClosed(ch <-chan struct{}, msg string) {
	select {
	case <-ch:
	case <-time.After(sweepWait):
		s.FailNow(msg)
	}
}

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
