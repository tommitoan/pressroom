package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tommitoan/pressroom/internal/api"
	"github.com/tommitoan/pressroom/internal/render"
)

// These tests put the real browser engine behind the real HTTP handler. They
// run only with PRESSROOM_E2E=1; see internal/render for the environment.

const e2eToken = "e2e-token-0123456789abcdef"

type stack struct {
	srv     *httptest.Server
	engine  *render.Chromium
	mu      sync.Mutex
	blocked []string
}

func newStack(t *testing.T, concurrency, queue int, renderTimeout time.Duration, maxBody int64) *stack {
	t.Helper()
	if os.Getenv("PRESSROOM_E2E") != "1" {
		t.Skip("set PRESSROOM_E2E=1 to run the browser tests")
	}
	s := &stack{}
	s.engine = render.NewChromium(render.ChromiumConfig{
		ExecPath:    os.Getenv("CHROME_PATH"),
		NoSandbox:   os.Getenv("CHROMIUM_NO_SANDBOX") == "true",
		Concurrency: concurrency,
		Queue:       queue,
		OnBlocked: func(u string) {
			s.mu.Lock()
			s.blocked = append(s.blocked, u)
			s.mu.Unlock()
		},
	})
	s.srv = httptest.NewServer(api.New(api.Deps{
		Renderer:      s.engine,
		Token:         e2eToken,
		MaxBodyBytes:  maxBody,
		RenderTimeout: renderTimeout,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(func() {
		s.srv.Close()
		s.engine.Close()
	})
	return s
}

type answer struct {
	status      int
	contentType string
	retryAfter  string
	body        []byte
	took        time.Duration
}

func (s *stack) post(t *testing.T, token, body string) answer {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.srv.URL+"/v1/pdf", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After"), b, time.Since(start)}
}

func (s *stack) ready(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(s.srv.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func jsonBody(html string, options map[string]any) string {
	b, _ := json.Marshal(map[string]any{"html": html, "options": options})
	return string(b)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../render/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (s *stack) mustRender(t *testing.T, html string) {
	t.Helper()
	a := s.post(t, e2eToken, jsonBody(html, nil))
	if a.status != 200 || !bytes.HasPrefix(a.body, []byte("%PDF-")) {
		t.Fatalf("the service no longer renders: status %d, body %.80q", a.status, a.body)
	}
}

// longDocument is slow enough to keep a render slot busy while a burst arrives.
func longDocument(pages int) string {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><style>section{break-after:page}td{border:1px solid #888;padding:2px}</style>")
	for p := 0; p < pages; p++ {
		b.WriteString("<section><table>")
		for r := 0; r < 40; r++ {
			fmt.Fprintf(&b, "<tr><td>page %d row %d</td><td>Thập thần Đại vận</td><td>lorem ipsum dolor sit amet</td></tr>", p, r)
		}
		b.WriteString("</table></section>")
	}
	return b.String()
}

func TestE2EBurstAboveTheQueueIsTurnedAwayFast(t *testing.T) {
	s := newStack(t, 1, 1, 30*time.Second, 2<<20)
	s.mustRender(t, "<p>warm</p>") // start the browser so the burst measures rendering, not launching

	const burst = 10
	body := jsonBody(longDocument(120), nil)
	answers := make([]answer, burst)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			answers[i] = s.post(t, e2eToken, body)
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, busy int
	var slowestRejection time.Duration
	for _, a := range answers {
		switch a.status {
		case 200:
			ok++
		case 429:
			busy++
			if a.took > slowestRejection {
				slowestRejection = a.took
			}
			if a.retryAfter == "" || !strings.Contains(string(a.body), `"code":"busy"`) {
				t.Errorf("429 without Retry-After or the busy code: %q (Retry-After %q)", a.body, a.retryAfter)
			}
		default:
			t.Errorf("unexpected status %d: %.120q", a.status, a.body)
		}
	}
	t.Logf("burst of %d with 1 running and 1 queued: %d rendered, %d turned away; slowest rejection %s",
		burst, ok, busy, slowestRejection.Round(time.Millisecond))
	if ok < 1 {
		t.Error("no request of the burst was served")
	}
	if busy < burst-4 { // 2 places plus slack for a slot freed mid-burst
		t.Errorf("only %d of %d requests were turned away; the queue is not bounded", busy, burst)
	}
	if slowestRejection > time.Second {
		t.Errorf("a rejection took %s; turning away must not wait", slowestRejection)
	}

	if got := s.ready(t); got != 200 {
		t.Errorf("/ready after the burst = %d, want 200", got)
	}
	s.mustRender(t, "<p>after the burst</p>")
}

func TestE2EHostilePageOverHTTP(t *testing.T) {
	s := newStack(t, 2, 2, 30*time.Second, 2<<20)
	a := s.post(t, e2eToken, jsonBody(fixture(t, "hostile.html"), nil))
	if a.status != 200 || a.contentType != "application/pdf" || !bytes.HasPrefix(a.body, []byte("%PDF-")) {
		t.Fatalf("status %d, type %q", a.status, a.contentType)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	joined := strings.Join(s.blocked, "\n")
	for _, host := range []string{"css.", "bg.", "pixel.", "frame.", "font."} {
		if !strings.Contains(joined, "http://"+host+"pressroom-test.invalid/") {
			t.Errorf("the request to %s was not intercepted; saw:\n%s", host, joined)
		}
	}
	for _, host := range []string{"fetch.", "onload."} {
		if strings.Contains(joined, host) {
			t.Errorf("a script ran: %s was requested", host)
		}
	}
}

func TestE2ERenderTimeoutOverHTTP(t *testing.T) {
	s := newStack(t, 1, 0, 40*time.Millisecond, 2<<20)
	warm, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.engine.Ready(warm); err != nil { // start the browser outside the short deadline
		t.Fatal(err)
	}

	a := s.post(t, e2eToken, jsonBody(longDocument(150), nil))
	if a.status != http.StatusGatewayTimeout || !strings.Contains(string(a.body), `"code":"render_timeout"`) {
		t.Fatalf("status %d, body %.120q; want 504 render_timeout", a.status, a.body)
	}
	if a.took > 2*time.Second {
		t.Errorf("the timeout answer took %s", a.took)
	}
	if got := s.ready(t); got != 200 {
		t.Errorf("/ready after a timeout = %d, want 200", got)
	}
	// The next request has the same short deadline, so ask the engine directly.
	pdf, err := s.engine.Render(warm, render.Request{HTML: "<p>after</p>", Options: render.Options{Paper: "A4", Scale: 1}})
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("the engine is not healthy after a timeout: %v", err)
	}
}

func TestE2EBadRequestsAreRefusedAndHarmless(t *testing.T) {
	s := newStack(t, 2, 2, 30*time.Second, 64<<10)

	valid := jsonBody("<p>x</p>", nil)
	for name, tc := range map[string]struct {
		token  string
		body   string
		status int
		code   string
	}{
		"no token":            {"", valid, 401, "unauthorized"},
		"wrong token":         {"not-the-token-0123456789", valid, 401, "unauthorized"},
		"malformed json":      {e2eToken, `{"html": `, 400, "invalid_json"},
		"not an object":       {e2eToken, `[1,2]`, 400, "invalid_json"},
		"unknown field":       {e2eToken, `{"html":"<p>x</p>","url":"http://example.invalid"}`, 400, "invalid_json"},
		"unknown option":      {e2eToken, `{"html":"<p>x</p>","options":{"javascript":true}}`, 400, "invalid_json"},
		"bad paper":           {e2eToken, jsonBody("<p>x</p>", map[string]any{"paper": "B5"}), 400, "invalid_options"},
		"scale out of range":  {e2eToken, jsonBody("<p>x</p>", map[string]any{"scale": 9}), 400, "invalid_options"},
		"empty html":          {e2eToken, `{"html":""}`, 400, "html_required"},
		"body over the limit": {e2eToken, jsonBody("<p>"+strings.Repeat("a", 70<<10)+"</p>", nil), 413, "payload_too_large"},
	} {
		a := s.post(t, tc.token, tc.body)
		if a.status != tc.status || !strings.Contains(string(a.body), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: status %d body %.140q; want %d %s", name, a.status, a.body, tc.status, tc.code)
		}
		if strings.Contains(string(a.body), e2eToken) {
			t.Errorf("%s: the answer repeats the token", name)
		}
	}

	if got := s.ready(t); got != 200 {
		t.Errorf("/ready = %d, want 200", got)
	}
	s.mustRender(t, "<p>still working</p>")
}
