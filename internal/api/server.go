// Package api implements the HTTP interface of pressroom.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/tommitoan/pressroom/internal/middleware"
	"github.com/tommitoan/pressroom/internal/render"
)

// Deps are the collaborators of the API.
type Deps struct {
	Renderer      render.Renderer
	Token         string
	MaxBodyBytes  int64
	RenderTimeout time.Duration
	Logger        *slog.Logger
}

// New returns the HTTP handler: health and readiness without authentication,
// and the PDF endpoint behind the bearer token.
func New(d Deps) http.Handler {
	h := &handlers{d: d}
	auth := middleware.BearerAuth(d.Token)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", only(http.MethodGet, h.health))
	mux.HandleFunc("/ready", only(http.MethodGet, h.ready))
	mux.Handle("/v1/pdf", auth(only(http.MethodPost, h.pdf)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		middleware.WriteError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	return middleware.Recover(d.Logger)(middleware.RequestLog(d.Logger)(mux))
}

// only restricts a handler to one method and answers others with a JSON 405.
func only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			middleware.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use "+method)
			return
		}
		next(w, r)
	}
}

type handlers struct{ d Deps }

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}

func (h *handlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.d.Renderer.Ready(ctx); err != nil {
		(&apiError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "renderer is not ready", RetryAfter: 5}).write(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ready"}` + "\n"))
}

func (h *handlers) pdf(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, h.d.MaxBodyBytes)
	req, aerr := decodeRequest(body)
	if aerr != nil {
		aerr.write(w)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.d.RenderTimeout)
	defer cancel()
	pdf, err := h.d.Renderer.Render(ctx, req)
	if err != nil {
		h.renderError(err).write(w)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pdf)
}

// renderError maps a renderer failure to the contract's status and code.
// Nothing from the error text is sent to the caller.
func (h *handlers) renderError(err error) *apiError {
	switch {
	case errors.Is(err, render.ErrBusy):
		return &apiError{Status: http.StatusTooManyRequests, Code: "busy", Message: "too many renders in progress", RetryAfter: 2}
	case errors.Is(err, render.ErrUnavailable):
		return &apiError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "renderer is unavailable", RetryAfter: 5}
	case errors.Is(err, context.DeadlineExceeded):
		return &apiError{Status: http.StatusGatewayTimeout, Code: "render_timeout", Message: "render took too long"}
	default:
		h.d.Logger.Error("render failed", "err", err.Error())
		return &apiError{Status: http.StatusInternalServerError, Code: "render_failed", Message: "render failed"}
	}
}
