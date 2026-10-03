package limits

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireAndReleaseWithinCapacity(t *testing.T) {
	l := New(2, 0)
	r1, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if l.InFlight() != 2 {
		t.Errorf("in flight = %d, want 2", l.InFlight())
	}
	r1()
	r2()
	if l.InFlight() != 0 {
		t.Errorf("in flight after release = %d, want 0", l.InFlight())
	}
}

func TestFullLimiterRejectsAtOnce(t *testing.T) {
	l := New(1, 1)
	r1, _ := l.Acquire(context.Background())
	// The second caller waits in the queue.
	waiting := make(chan error, 1)
	go func() {
		r, err := l.Acquire(context.Background())
		if err == nil {
			r()
		}
		waiting <- err
	}()
	waitFor(t, func() bool { return l.InFlight() == 2 })

	start := time.Now()
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrFull) {
		t.Fatalf("third caller: error = %v, want ErrFull", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Error("a rejected caller must not wait")
	}

	r1()
	if err := <-waiting; err != nil {
		t.Errorf("the queued caller failed: %v", err)
	}
}

func TestZeroQueueRejectsWhenBusy(t *testing.T) {
	l := New(1, 0)
	r, _ := l.Acquire(context.Background())
	defer r()
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrFull) {
		t.Errorf("error = %v, want ErrFull", err)
	}
}

func TestContextEndsWhileWaiting(t *testing.T) {
	l := New(1, 1)
	r, _ := l.Acquire(context.Background())
	defer r()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	// The abandoned queue place must be free again.
	if l.InFlight() != 1 {
		t.Errorf("in flight = %d, want 1 after the waiter gave up", l.InFlight())
	}
	// ...so a new waiter is admitted again instead of being turned away.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if _, err := l.Acquire(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want DeadlineExceeded (admitted, then timed out)", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	l := New(1, 0)
	r, _ := l.Acquire(context.Background())
	r()
	r()
	r2, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("a double release broke the limiter: %v", err)
	}
	r2()
	if l.InFlight() != 0 {
		t.Errorf("in flight = %d, want 0", l.InFlight())
	}
}

func TestNeverExceedsConcurrency(t *testing.T) {
	const concurrency, workers = 3, 40
	l := New(concurrency, workers)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			running.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if peak.Load() > concurrency {
		t.Errorf("peak concurrency = %d, want at most %d", peak.Load(), concurrency)
	}
	if l.InFlight() != 0 {
		t.Errorf("in flight = %d, want 0", l.InFlight())
	}
}

func TestInvalidSizesAreClamped(t *testing.T) {
	l := New(0, -5)
	r, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(time.Millisecond)
	}
}
