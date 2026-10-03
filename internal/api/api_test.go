package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tommitoan/pressroom/internal/render"
)

const (
	token     = "0123456789abcdef"
	samplePDF = "%PDF-1.7 fake"
)

type env struct {
	h    http.Handler
	fake *render.Fake
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env { return newEnvWithLimit(t, 4096) }

func newEnvWithLimit(t *testing.T, maxBody int64) *env {
	t.Helper()
	fake := &render.Fake{PDF: []byte(samplePDF)}
	logs := &bytes.Buffer{}
	return &env{
		fake: fake, logs: logs,
		h: New(Deps{
			Renderer: fake, Token: token, MaxBodyBytes: maxBody, RenderTimeout: 3 * time.Second,
			Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		}),
	}
}

func (e *env) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/pdf", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not a JSON error: %q", rec.Body.String())
	}
	return body["code"], body["error"]
}

func TestPDFSuccessAppliesDefaults(t *testing.T) {
	e := newEnv(t)
	rec := e.post(`{"html":"<p>hello</p>"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/pdf" || rec.Body.String() != samplePDF ||
		rec.Header().Get("Content-Length") != fmt.Sprint(len(samplePDF)) {
		t.Errorf("unexpected response: %v %q", rec.Header(), rec.Body.String())
	}
	calls := e.fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("renderer called %d times", len(calls))
	}
	want := render.Options{Paper: "A4", PrintBackground: true, PreferCSSPageSize: true, Scale: 1,
		Margins: render.Margins{Top: 15, Right: 12, Bottom: 18, Left: 12}}
	if calls[0].HTML != "<p>hello</p>" || calls[0].Options != want {
		t.Errorf("request = %+v, want defaults %+v", calls[0], want)
	}
}

func TestPDFPassesExplicitOptions(t *testing.T) {
	e := newEnv(t)
	rec := e.post(`{"html":"x","options":{"paper":"Letter","landscape":true,"print_background":false,
		"prefer_css_page_size":false,"scale":1.5,"margins_mm":{"top":0,"left":50},
		"header_html":"<b>h</b>","footer_html":"<i>f</i>"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	got := e.fake.Calls()[0].Options
	want := render.Options{Paper: "Letter", Landscape: true, PrintBackground: false, PreferCSSPageSize: false, Scale: 1.5,
		Margins: render.Margins{Top: 0, Right: 12, Bottom: 18, Left: 50}, HeaderHTML: "<b>h</b>", FooterHTML: "<i>f</i>"}
	if got != want {
		t.Errorf("options = %+v, want %+v (unset margins keep their defaults)", got, want)
	}
}

func TestPDFValidation(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"not json":            {`nope`, 400, "invalid_json"},
		"empty body":          {``, 400, "invalid_json"},
		"array":               {`[]`, 400, "invalid_json"},
		"unknown top field":   {`{"html":"x","extra":1}`, 400, "invalid_json"},
		"unknown option":      {`{"html":"x","options":{"colour":true}}`, 400, "invalid_json"},
		"wrong type":          {`{"html":123}`, 400, "invalid_json"},
		"second object":       {`{"html":"x"} {"html":"y"}`, 400, "invalid_json"},
		"trailing garbage":    {`{"html":"x"} nope`, 400, "invalid_json"},
		"html missing":        {`{}`, 400, "html_required"},
		"html blank":          {`{"html":"  \n "}`, 400, "html_required"},
		"bad paper":           {`{"html":"x","options":{"paper":"B5"}}`, 400, "invalid_options"},
		"scale too small":     {`{"html":"x","options":{"scale":0.4}}`, 400, "invalid_options"},
		"scale too large":     {`{"html":"x","options":{"scale":2.1}}`, 400, "invalid_options"},
		"negative margin":     {`{"html":"x","options":{"margins_mm":{"top":-1}}}`, 400, "invalid_options"},
		"margin too large":    {`{"html":"x","options":{"margins_mm":{"left":50.5}}}`, 400, "invalid_options"},
		"body over the limit": {`{"html":"` + strings.Repeat("a", 5000) + `"}`, 413, "payload_too_large"},
	} {
		rec := e.post(tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %s)", name, rec.Code, tc.status, rec.Body)
			continue
		}
		if code, _ := errorOf(t, rec); code != tc.code {
			t.Errorf("%s: code = %q, want %q", name, code, tc.code)
		}
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("the renderer ran %d times for invalid input", n)
	}
}

// With a body limit above 16 KiB the template size check itself is reached.
func TestHeaderAndFooterSizeLimit(t *testing.T) {
	e := newEnvWithLimit(t, 1<<20)
	atLimit := strings.Repeat("x", maxTemplateBytes)
	over := atLimit + "y"
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"header at the limit": {fmt.Sprintf(`{"html":"x","options":{"header_html":%q}}`, atLimit), 200},
		"footer at the limit": {fmt.Sprintf(`{"html":"x","options":{"footer_html":%q}}`, atLimit), 200},
		"header over":         {fmt.Sprintf(`{"html":"x","options":{"header_html":%q}}`, over), 400},
		"footer over":         {fmt.Sprintf(`{"html":"x","options":{"footer_html":%q}}`, over), 400},
	} {
		rec := e.post(tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.status)
			continue
		}
		if tc.status == 400 {
			if code, _ := errorOf(t, rec); code != "invalid_options" {
				t.Errorf("%s: code = %q, want invalid_options", name, code)
			}
		}
	}
}

func TestErrorMessagesNeverEchoTheRequest(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{
		`{"html":"PRIVATE-DATA","options":{"paper":"PRIVATE-PAPER"}}`,
		`{"html":"PRIVATE-DATA","unknown_PRIVATE":1}`,
		`PRIVATE-DATA`,
	} {
		rec := e.post(body)
		if strings.Contains(rec.Body.String(), "PRIVATE") {
			t.Errorf("response to %q echoes the request: %s", body, rec.Body)
		}
	}
}

func TestRendererErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		status     int
		code       string
		retryAfter string
	}{
		"busy":        {render.ErrBusy, 429, "busy", "2"},
		"unavailable": {render.ErrUnavailable, 503, "unavailable", "5"},
		"wrapped":     {fmt.Errorf("start: %w", render.ErrUnavailable), 503, "unavailable", "5"},
		"timeout":     {context.DeadlineExceeded, 504, "render_timeout", ""},
		"other":       {errors.New("chromium crashed: PRIVATE-DETAIL"), 500, "render_failed", ""},
	} {
		e := newEnv(t)
		e.fake.Err = tc.err
		rec := e.post(`{"html":"x"}`)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.status)
			continue
		}
		if code, _ := errorOf(t, rec); code != tc.code {
			t.Errorf("%s: code = %q, want %q", name, code, tc.code)
		}
		if got := rec.Header().Get("Retry-After"); got != tc.retryAfter {
			t.Errorf("%s: Retry-After = %q, want %q", name, got, tc.retryAfter)
		}
		if strings.Contains(rec.Body.String(), "PRIVATE-DETAIL") {
			t.Errorf("%s: the renderer's error text leaked", name)
		}
	}
}

func TestRenderGetsADeadline(t *testing.T) {
	e := newEnv(t)
	start := time.Now()
	e.post(`{"html":"x"}`)
	deadline, ok := e.fake.Deadline()
	if !ok || deadline.Before(start) || deadline.After(start.Add(4*time.Second)) {
		t.Errorf("deadline = %v (set %v), want about 3 s from now", deadline, ok)
	}
}

func TestAuthIsRequiredForPDFOnly(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/pdf", strings.NewReader(`{"html":"x"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("the renderer ran %d times without a token", n)
	}
	for _, path := range []string{"/health", "/ready"} {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s without a token: status = %d, want 200", path, rec.Code)
		}
	}
}

func TestReadyReportsTheRenderer(t *testing.T) {
	e := newEnv(t)
	e.fake.ReadyErr = errors.New("no browser")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if strings.Contains(rec.Body.String(), "no browser") {
		t.Error("the renderer's error text leaked")
	}
}

func TestWrongMethodsAndUnknownPaths(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{http.MethodGet, "/v1/pdf", 405, "POST"},
		{http.MethodPost, "/health", 405, "GET"},
		{http.MethodPost, "/ready", 405, "GET"},
		{http.MethodGet, "/nope", 404, ""},
		{http.MethodGet, "/v1/other", 404, ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s: status = %d, Allow = %q, want %d and %q", tc.method, tc.path, rec.Code, rec.Header().Get("Allow"), tc.status, tc.allow)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: error body is not JSON", tc.method, tc.path)
		}
	}
}

func TestRequestContentIsNeverLogged(t *testing.T) {
	e := newEnv(t)
	e.post(`{"html":"<p>PRIVATE-BIRTH-DATA</p>","options":{"footer_html":"PRIVATE-FOOTER"}}`)
	e.post(`{"html":"PRIVATE-BIRTH-DATA","unknown":1}`)
	e.fake.Err = errors.New("renderer failed")
	e.post(`{"html":"PRIVATE-BIRTH-DATA"}`)
	out := e.logs.String()
	if !strings.Contains(out, `"path":"/v1/pdf"`) {
		t.Fatalf("no request was logged: %s", out)
	}
	for _, leaked := range []string{"PRIVATE-BIRTH-DATA", "PRIVATE-FOOTER", token} {
		if strings.Contains(out, leaked) {
			t.Errorf("the log contains %q", leaked)
		}
	}
}

type panicRenderer struct{}

func (panicRenderer) Render(context.Context, render.Request) ([]byte, error) { panic("boom PRIVATE") }
func (panicRenderer) Ready(context.Context) error                            { return nil }

func TestRendererPanicBecomesInternalError(t *testing.T) {
	h := New(Deps{Renderer: panicRenderer{}, Token: token, MaxBodyBytes: 4096, RenderTimeout: time.Second,
		Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))})
	req := httptest.NewRequest(http.MethodPost, "/v1/pdf", strings.NewReader(`{"html":"x"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Errorf("status = %d body %s", rec.Code, rec.Body)
	}
}
