package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestServeDrainsRequestsInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})
	ln := listen(t)
	srv := newServer(ln.Addr().String(), h, 20*time.Second)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopped := make(chan error, 1)
	go func() { stopped <- serve(ctx, srv, ln, 5*time.Second) }()

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- result{body: string(b)}
	}()
	<-started

	stop() // the termination signal arrives while the request is running

	select {
	case err := <-stopped:
		t.Fatalf("serve returned (%v) while a request was still running", err)
	case <-time.After(150 * time.Millisecond):
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		conn.Close()
		t.Error("the server still accepts new connections after shutdown began")
	}

	close(release)
	if r := <-got; r.err != nil || r.body != "finished" {
		t.Fatalf("the request in flight was cut short: body %q, error %v", r.body, r.err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("serve returned %v after a clean drain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after the last request finished")
	}
}

func TestServeGivesUpWhenTheDrainTimeIsUp(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})
	ln := listen(t)
	srv := newServer(ln.Addr().String(), h, 20*time.Second)
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- serve(ctx, srv, ln, 150*time.Millisecond) }()

	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String()); err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	stop()

	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve ignored the drain limit")
	}
	_ = srv.Close()
}

func TestServeStopsPromptlyWhenIdle(t *testing.T) {
	ln := listen(t)
	srv := newServer(ln.Addr().String(), http.NotFoundHandler(), 20*time.Second)
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- serve(ctx, srv, ln, 5*time.Second) }()
	stop()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an idle server did not stop")
	}
}

func TestServeReportsAListenerThatFails(t *testing.T) {
	ln := listen(t)
	srv := newServer(ln.Addr().String(), http.NotFoundHandler(), 20*time.Second)
	ln.Close()
	err := serve(context.Background(), srv, ln, time.Second)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Errorf("error = %v, want the listener error", err)
	}
}

func TestOversizedHeadersAreRefused(t *testing.T) {
	ln := listen(t)
	srv := newServer(ln.Addr().String(), http.NotFoundHandler(), 20*time.Second)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, time.Second) }()
	defer func() { stop(); <-done }()

	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String(), nil)
	// net/http reads 4 KiB beyond MaxHeaderBytes before it refuses, so go well over.
	req.Header.Set("X-Padding", strings.Repeat("a", maxHeaderBytes+8<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status = %d, want 431", resp.StatusCode)
	}

	small, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String(), nil)
	small.Header.Set("X-Padding", strings.Repeat("a", 4<<10))
	resp, err = http.DefaultClient.Do(small)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a 4 KiB header was refused: status %d", resp.StatusCode)
	}
}

func TestWriteTimeoutOutlastsTheRenderDeadline(t *testing.T) {
	for _, renderTimeout := range []time.Duration{time.Second, 20 * time.Second, 2 * time.Minute} {
		srv := newServer(":0", http.NotFoundHandler(), renderTimeout)
		if srv.WriteTimeout <= renderTimeout {
			t.Errorf("write timeout %s must exceed the render timeout %s", srv.WriteTimeout, renderTimeout)
		}
		if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.IdleTimeout == 0 {
			t.Error("every read and idle timeout must be set")
		}
	}
}
