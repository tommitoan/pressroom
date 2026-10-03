package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "client-test-token-0123456789"

var fakePDF = []byte("%PDF-1.4 fake")

func pdfServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", testToken) // the trailing slash must be tolerated
}

func writeError(w http.ResponseWriter, status int, code, msg, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

func TestPDFSendsTheDocumentedRequest(t *testing.T) {
	c := pdfServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/pdf" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		if body["html"] != "<p>hi</p>" {
			t.Errorf("html = %v", body["html"])
		}
		if _, present := body["options"]; present {
			t.Error("options must be left out when nil, so the service defaults apply")
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(fakePDF)
	})
	got, err := c.PDF(context.Background(), "<p>hi</p>", nil)
	if err != nil || string(got) != string(fakePDF) {
		t.Fatalf("PDF = %q, %v", got, err)
	}
}

func TestOptionsKeepMeaningfulZeroValues(t *testing.T) {
	zero, off := 0.0, false
	c := pdfServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		opts, _ := body["options"].(map[string]any)
		margins, _ := opts["margins_mm"].(map[string]any)
		if v, ok := margins["top"]; !ok || v != 0.0 {
			t.Errorf("a margin of 0 must be sent, got %v", margins)
		}
		if _, ok := margins["left"]; ok {
			t.Errorf("an unset margin must be left out, got %v", margins)
		}
		if v, ok := opts["print_background"]; !ok || v != false {
			t.Errorf("print_background=false must be sent, got %v", opts)
		}
		if opts["paper"] != "A3" || opts["landscape"] != true || opts["footer_html"] != "<i></i>" {
			t.Errorf("options = %v", opts)
		}
		_, _ = w.Write(fakePDF)
	})
	_, err := c.PDF(context.Background(), "<p>x</p>", &Options{
		Paper: "A3", Landscape: true, FooterHTML: "<i></i>",
		MarginsMM: &Margins{Top: &zero}, PrintBackground: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestErrorsCarryTheServiceCode(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		temporary bool
	}{
		{400, "invalid_options", false},
		{401, "unauthorized", false},
		{413, "payload_too_large", false},
		{504, "render_timeout", false},
		{500, "render_failed", false},
	} {
		c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writeError(w, tc.status, tc.code, "message for a developer", "")
		})
		_, err := c.PDF(context.Background(), "<p>x</p>", nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Code != tc.code || apiErr.Temporary() != tc.temporary {
			t.Errorf("status %d: error = %#v", tc.status, err)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Error("the error text contains the token")
		}
	}
}

func TestAnAnswerFromAProxyIsStillAnAPIError(t *testing.T) {
	c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>upstream sent too short response</html>")
	})
	_, err := c.PDF(context.Background(), "<p>x</p>", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Code != "" || strings.Contains(apiErr.Message, "<html>") {
		t.Fatalf("error = %#v", err)
	}
}

func TestBusyAndUnavailableAreRetriedAfterTheRequestedWait(t *testing.T) {
	var calls atomic.Int32
	c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		switch calls.Add(1) {
		case 1:
			writeError(w, 429, "busy", "queue is full", "0")
		case 2:
			writeError(w, 503, "unavailable", "browser starting", "0")
		default:
			_, _ = w.Write(fakePDF)
		}
	})
	got, err := c.PDF(context.Background(), "<p>x</p>", nil)
	if err != nil || string(got) != string(fakePDF) || calls.Load() != 3 {
		t.Fatalf("got %q, %v after %d calls", got, err, calls.Load())
	}
}

func TestRetriesAreLimited(t *testing.T) {
	var calls atomic.Int32
	c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		calls.Add(1)
		writeError(w, 429, "busy", "queue is full", "0")
	})
	c.Retries = 1
	_, err := c.PDF(context.Background(), "<p>x</p>", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "busy" || calls.Load() != 2 {
		t.Fatalf("error = %v after %d calls, want busy after 2", err, calls.Load())
	}
}

func TestOtherErrorsAreNeverRetried(t *testing.T) {
	for _, status := range []int{400, 401, 413, 500, 504} {
		var calls atomic.Int32
		c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			calls.Add(1)
			writeError(w, status, "x", "no", "0")
		})
		_, _ = c.PDF(context.Background(), "<p>x</p>", nil)
		if calls.Load() != 1 {
			t.Errorf("status %d was tried %d times", status, calls.Load())
		}
	}
}

func TestWaitingForRetryAfterStopsWhenTheContextEnds(t *testing.T) {
	c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeError(w, 429, "busy", "queue is full", "5")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.PDF(ctx, "<p>x</p>", nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("error = %v after %s", err, time.Since(start))
	}
}

func TestRetryAfterParsing(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"2": 2 * time.Second, "0": 0, "": time.Second, "soon": time.Second, "-3": time.Second, "3600": maxRetryWait,
	} {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestReadyAndHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("probes need no token and must not send one")
		}
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"browser not responding","code":"unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, testToken)
	if err := c.Health(context.Background()); err != nil {
		t.Errorf("Health = %v", err)
	}
	var apiErr *APIError
	if err := c.Ready(context.Background()); !errors.As(err, &apiErr) || apiErr.Code != "unavailable" {
		t.Errorf("Ready = %v, want the unavailable answer", err)
	}
}

func TestAnUnreachableServiceIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := New(url, testToken)
	if _, err := c.PDF(context.Background(), "<p>x</p>", nil); err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("error = %v", err)
	}
}
