package lock

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestLocker_SerializesSameKey(t *testing.T) {
	locker := NewLocker()

	const goroutines = 50

	var (
		active  atomic.Int32
		maxSeen atomic.Int32
		wg      sync.WaitGroup
	)

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			unlock, err := locker.Lock(t.Context(), "same-key")
			if err != nil {
				t.Errorf("unexpected error: %v", err)

				return
			}
			defer unlock()

			n := active.Add(1)

			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}

			active.Add(-1)
		}()
	}

	wg.Wait()

	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("want at most 1 concurrent holder of the same key, saw %d", got)
	}
}

func TestLocker_DifferentKeysDoNotBlockEachOther(t *testing.T) {
	locker := NewLocker()

	unlockA, err := locker.Lock(t.Context(), "a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer unlockA()

	done := make(chan struct{})

	go func() {
		unlockB, err := locker.Lock(t.Context(), "b")
		if err != nil {
			t.Errorf("unexpected error: %v", err)

			return
		}
		defer unlockB()

		close(done)
	}()

	<-done
}
