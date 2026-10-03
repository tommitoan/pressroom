package render

import (
	"context"
	"sync"
	"time"
)

// Fake is a Renderer for tests. It records what it was asked to render and
// returns the configured PDF or error.
type Fake struct {
	PDF      []byte
	Err      error
	ReadyErr error

	mu          sync.Mutex
	calls       []Request
	hadDeadline bool
	deadline    time.Time
}

// Render records req and the deadline of ctx, then returns the configured result.
func (f *Fake) Render(ctx context.Context, req Request) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.deadline, f.hadDeadline = ctx.Deadline()
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	return f.PDF, nil
}

// Ready returns the configured readiness error.
func (f *Fake) Ready(context.Context) error { return f.ReadyErr }

// Calls returns the requests rendered so far.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

// Deadline returns the deadline of the last Render context.
func (f *Fake) Deadline() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadline, f.hadDeadline
}
