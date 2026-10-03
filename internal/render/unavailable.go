package render

import "context"

// Unavailable is the renderer used until a browser engine is wired in: it
// answers honestly that no render can run, so the service never pretends.
type Unavailable struct{}

// Render always fails with ErrUnavailable.
func (Unavailable) Render(context.Context, Request) ([]byte, error) { return nil, ErrUnavailable }

// Ready always fails with ErrUnavailable.
func (Unavailable) Ready(context.Context) error { return ErrUnavailable }
