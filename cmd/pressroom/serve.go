package main

import (
	"context"
	"net"
	"net/http"
	"time"
)

// maxHeaderBytes caps request headers; clearly larger ones are answered with
// 431 (net/http tolerates 4 KiB beyond the cap).
const maxHeaderBytes = 16 << 10

// newServer returns the HTTP server with its timeouts. The write timeout
// outlasts the render deadline so a render that finishes at its deadline can
// still deliver its answer.
func newServer(addr string, handler http.Handler, renderTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		MaxHeaderBytes:    maxHeaderBytes,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      renderTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// serve runs srv on ln until ctx ends, then stops accepting connections and
// waits up to drain for requests in flight. It returns once the server has
// stopped, so the caller can release resources the handlers use (the browser)
// without cutting a render short.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	<-served
	return err
}
