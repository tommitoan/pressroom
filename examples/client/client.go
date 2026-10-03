// Package client is a small, dependency-free Go client for pressroom. It is
// meant to be copied into the service that needs PDFs: only the standard
// library is used, and the package name can be changed freely.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxPDFBytes bounds how much of a response body is read.
const maxPDFBytes = 64 << 20

// maxRetryWait caps how long a Retry-After header can make the client wait.
const maxRetryWait = 10 * time.Second

// Margins are page margins in millimetres. A nil field keeps the service default.
type Margins struct {
	Top    *float64 `json:"top,omitempty"`
	Right  *float64 `json:"right,omitempty"`
	Bottom *float64 `json:"bottom,omitempty"`
	Left   *float64 `json:"left,omitempty"`
}

// Options are the print options of docs/API.md. Zero values mean "use the
// service default", except where a pointer is used because false or 0 is a
// meaningful choice.
type Options struct {
	Paper             string   `json:"paper,omitempty"`
	Landscape         bool     `json:"landscape,omitempty"`
	MarginsMM         *Margins `json:"margins_mm,omitempty"`
	PrintBackground   *bool    `json:"print_background,omitempty"`
	PreferCSSPageSize *bool    `json:"prefer_css_page_size,omitempty"`
	Scale             float64  `json:"scale,omitempty"`
	HeaderHTML        string   `json:"header_html,omitempty"`
	FooterHTML        string   `json:"footer_html,omitempty"`
}

// Client talks to one pressroom service.
type Client struct {
	// BaseURL is the service address, for example "http://pressroom.railway.internal:8080".
	BaseURL string
	// Token is the shared bearer token.
	Token string
	// HTTP is the transport; New sets one with a timeout above the service's render limit.
	HTTP *http.Client
	// Retries is how many times a busy (429) or unavailable (503) answer is
	// retried after waiting for Retry-After. Other errors are never retried.
	Retries int
}

// New returns a client with a 30 second request timeout and two retries.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Retries: 2,
	}
}

// APIError is an error answer from the service.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the machine-readable code of docs/API.md (empty when the answer
	// did not come from pressroom, for example from a proxy).
	Code string
	// Message is safe to show a developer; it never contains the HTML.
	Message string
	// RetryAfter is the wait the service asked for, if any.
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("pressroom: status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("pressroom: %s (status %d): %s", e.Code, e.Status, e.Message)
}

// Temporary reports whether trying again later may succeed: the queue was
// full or the renderer was not available.
func (e *APIError) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable
}

// PDF renders html and returns the PDF. opts may be nil.
func (c *Client) PDF(ctx context.Context, html string, opts *Options) ([]byte, error) {
	body, err := json.Marshal(struct {
		HTML    string   `json:"html"`
		Options *Options `json:"options,omitempty"`
	}{html, opts})
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		pdf, err := c.once(ctx, body)
		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || !apiErr.Temporary() || attempt >= c.Retries {
			return pdf, err
		}
		if err := sleep(ctx, apiErr.RetryAfter); err != nil {
			return nil, err
		}
	}
}

func (c *Client) once(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/pdf", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pressroom: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	pdf, err := io.ReadAll(io.LimitReader(resp.Body, maxPDFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pressroom: reading the PDF: %w", err)
	}
	if len(pdf) > maxPDFBytes {
		return nil, errors.New("pressroom: the PDF is larger than the client accepts")
	}
	return pdf, nil
}

// Ready reports whether the service can render now (GET /ready).
func (c *Client) Ready(ctx context.Context) error { return c.probe(ctx, "/ready") }

// Health reports whether the service process is up (GET /health).
func (c *Client) Health(ctx context.Context) error { return c.probe(ctx, "/health") }

func (c *Client) probe(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("pressroom: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readAPIError(resp)
	}
	return nil
}

func readAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var parsed struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Code != "" {
		e.Code, e.Message = parsed.Code, parsed.Error
		return e
	}
	e.Message = http.StatusText(resp.StatusCode)
	return e
}

func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return time.Second
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryWait {
		return maxRetryWait
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
