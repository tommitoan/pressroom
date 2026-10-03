// Package render defines what the API needs from a PDF renderer. The browser
// based implementation lives next to it; the API and its tests depend only on
// this interface.
package render

import (
	"context"
	"errors"
)

var (
	// ErrBusy means the render queue is full; the caller may retry soon.
	ErrBusy = errors.New("render queue is full")
	// ErrUnavailable means the renderer cannot run renders right now.
	ErrUnavailable = errors.New("renderer unavailable")
)

// Margins are page margins in millimetres.
type Margins struct {
	Top, Right, Bottom, Left float64
}

// Options control how a document is printed.
type Options struct {
	Paper             string
	Landscape         bool
	Margins           Margins
	PrintBackground   bool
	PreferCSSPageSize bool
	Scale             float64
	HeaderHTML        string
	FooterHTML        string
}

// Request is one document to print.
type Request struct {
	HTML    string
	Options Options
}

// Renderer turns a self-contained HTML document into a PDF.
type Renderer interface {
	// Render prints req. It must stop when ctx is done.
	Render(ctx context.Context, req Request) ([]byte, error)
	// Ready reports whether a render can start now.
	Ready(ctx context.Context) error
}
