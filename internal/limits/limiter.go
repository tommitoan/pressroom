// Package limits bounds how much work runs at once.
package limits

import (
	"context"
	"errors"
	"sync"
)

// ErrFull means every running slot and every queue place is taken.
var ErrFull = errors.New("limiter is full")

// Limiter lets at most `concurrency` holders run, lets at most `queue` more
// wait behind them, and turns everyone else away at once instead of making
// them wait without bound.
type Limiter struct {
	admit chan struct{} // running and waiting holders together
	run   chan struct{} // running holders
}

// New returns a Limiter. concurrency must be at least 1; queue may be 0.
func New(concurrency, queue int) *Limiter {
	if concurrency < 1 {
		concurrency = 1
	}
	if queue < 0 {
		queue = 0
	}
	return &Limiter{
		admit: make(chan struct{}, concurrency+queue),
		run:   make(chan struct{}, concurrency),
	}
}

// Acquire takes a running slot, waiting in the queue if every slot is busy.
// It returns ErrFull immediately when the queue is also full, and ctx.Err()
// if ctx ends while waiting. The returned release func must be called exactly
// when the work is done; calling it more than once is harmless.
func (l *Limiter) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case l.admit <- struct{}{}:
	default:
		return nil, ErrFull
	}

	select {
	case l.run <- struct{}{}:
	case <-ctx.Done():
		<-l.admit
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.run
			<-l.admit
		})
	}, nil
}

// InFlight reports how many holders are running or waiting.
func (l *Limiter) InFlight() int { return len(l.admit) }
