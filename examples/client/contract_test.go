package client

// The contract test checks what a caller can rely on, using plain HTTP only so
// it does not depend on the client in this directory. Point it at a running
// service:
//
//	PRESSROOM_URL=http://localhost:8080 PRESSROOM_TOKEN=<token> go test -run Contract -v
//
// Copy it, with client.go, into a service that depends on pressroom and run it
// after changing either side. It sends one valid document per case and never
// reads anything but the answers.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const marker = "CONTRACT-MARKER-4f1c"

type reply struct {
	status int
	header http.Header
	body   []byte
}

func contractTarget(t *testing.T) (base, token string) {
	t.Helper()
	base, token = strings.TrimRight(os.Getenv("PRESSROOM_URL"), "/"), os.Getenv("PRESSROOM_TOKEN")
	if base == "" || token == "" {
		t.Skip("set PRESSROOM_URL and PRESSROOM_TOKEN to run the contract test")
	}
	return base, token
}

func call(t *testing.T, method, url, bearer, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, b}
}

func doc(html string, options map[string]any) string {
	m := map[string]any{"html": html}
	if options != nil {
		m["options"] = options
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func wantError(t *testing.T, name string, r reply, status int, code string, secrets ...string) {
	t.Helper()
	var e struct{ Error, Code string }
	if r.status != status || json.Unmarshal(r.body, &e) != nil || e.Code != code || e.Error == "" {
		t.Errorf("%s: status %d body %.160q; want %d with code %q and a message", name, r.status, r.body, status, code)
	}
	if !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
		t.Errorf("%s: error content type = %q", name, r.header.Get("Content-Type"))
	}
	for _, s := range secrets {
		if strings.Contains(string(r.body), s) {
			t.Errorf("%s: the answer repeats %q", name, s)
		}
	}
}

func TestContractProbes(t *testing.T) {
	base, _ := contractTarget(t)
	if r := call(t, http.MethodGet, base+"/health", "", ""); r.status != 200 || !strings.Contains(string(r.body), `"ok"`) {
		t.Errorf("/health = %d %q", r.status, r.body)
	}
	if r := call(t, http.MethodGet, base+"/ready", "", ""); r.status != 200 {
		t.Errorf("/ready = %d %q; the service cannot render", r.status, r.body)
	}
}

func TestContractAuthentication(t *testing.T) {
	base, token := contractTarget(t)
	valid := doc("<p>x</p>", nil)
	wantError(t, "no token", call(t, http.MethodPost, base+"/v1/pdf", "", valid), 401, "unauthorized")
	wantError(t, "wrong token", call(t, http.MethodPost, base+"/v1/pdf", "not-the-token-0123456789", valid), 401, "unauthorized", token)
}

func TestContractRenders(t *testing.T) {
	base, token := contractTarget(t)
	footer := `<div style="font-size:8px;width:100%;text-align:center"><span class="pageNumber"></span> / <span class="totalPages"></span></div>`
	for name, body := range map[string]string{
		"defaults only":      doc("<!doctype html><p>hello</p>", nil),
		"han and vietnamese": doc("<!doctype html><meta charset=utf-8><p style='font-family:\"Noto Serif\",\"Noto Serif CJK SC\",serif'>Bát tự 甲子 Đại vận</p>", nil),
		"every option": doc("<!doctype html><p>options</p>", map[string]any{
			"paper": "A3", "landscape": true, "margins_mm": map[string]any{"top": 0, "left": 20},
			"print_background": false, "prefer_css_page_size": false, "scale": 0.8, "footer_html": footer,
		}),
		"remote content is not fetched": doc(`<!doctype html><link rel=stylesheet href="http://contract.invalid/a.css"><img src="http://contract.invalid/a.png"><p>offline</p>`, nil),
	} {
		start := time.Now()
		r := call(t, http.MethodPost, base+"/v1/pdf", token, body)
		if r.status != 200 || r.header.Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(r.body, []byte("%PDF-")) {
			t.Errorf("%s: status %d type %q body %.80q", name, r.status, r.header.Get("Content-Type"), r.body)
			continue
		}
		if cl := r.header.Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(r.body)) {
			t.Errorf("%s: Content-Length %s but %d bytes received", name, cl, len(r.body))
		}
		if time.Since(start) > 20*time.Second {
			t.Errorf("%s: took %s, over the render limit", name, time.Since(start))
		}
	}
}

func TestContractRejectsBadRequestsWithoutEchoingThem(t *testing.T) {
	base, token := contractTarget(t)
	post := func(body string) reply { return call(t, http.MethodPost, base+"/v1/pdf", token, body) }

	wantError(t, "malformed json", post(`{"html": `), 400, "invalid_json")
	wantError(t, "not an object", post(`["`+marker+`"]`), 400, "invalid_json", marker)
	wantError(t, "unknown field", post(`{"html":"<p>`+marker+`</p>","url":"http://example.invalid"}`), 400, "invalid_json", marker)
	wantError(t, "unknown option", post(doc("<p>"+marker+"</p>", map[string]any{"javascript": true})), 400, "invalid_json", marker)
	wantError(t, "html missing", post(`{"options":{}}`), 400, "html_required")
	wantError(t, "html blank", post(`{"html":"   "}`), 400, "html_required")
	wantError(t, "unknown paper", post(doc("<p>"+marker+"</p>", map[string]any{"paper": "B5"})), 400, "invalid_options", marker)
	wantError(t, "scale out of range", post(doc("<p>"+marker+"</p>", map[string]any{"scale": 9})), 400, "invalid_options", marker)
	wantError(t, "margin out of range", post(doc("<p>"+marker+"</p>", map[string]any{"margins_mm": map[string]any{"top": 99}})), 400, "invalid_options", marker)
	wantError(t, "footer over 16 KiB", post(doc("<p>x</p>", map[string]any{"footer_html": strings.Repeat("a", 17<<10)})), 400, "invalid_options")
}

func TestContractRoutes(t *testing.T) {
	base, token := contractTarget(t)
	wantError(t, "unknown path", call(t, http.MethodGet, base+"/nope", "", ""), 404, "not_found")
	r := call(t, http.MethodGet, base+"/v1/pdf", token, "")
	wantError(t, "GET on the render endpoint", r, 405, "method_not_allowed")
	if r.header.Get("Allow") != http.MethodPost {
		t.Errorf("Allow = %q, want POST", r.header.Get("Allow"))
	}
}

// TestContractClient runs the client of this directory against the service, so
// a change on either side that breaks them apart is caught.
func TestContractClient(t *testing.T) {
	base, token := contractTarget(t)
	ctx := context.Background()
	c := New(base, token)

	if err := c.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	zero := 0.0
	pdf, err := c.PDF(ctx, "<!doctype html><p>client</p>", &Options{
		Paper: "Letter", MarginsMM: &Margins{Top: &zero},
		FooterHTML: `<span class="pageNumber"></span>`,
	})
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("PDF: %v (%.20q)", err, pdf)
	}

	var apiErr *APIError
	if _, err := c.PDF(ctx, "<p>x</p>", &Options{Paper: "B5"}); !errors.As(err, &apiErr) || apiErr.Code != "invalid_options" || apiErr.Temporary() {
		t.Errorf("a bad option: error = %v", err)
	}
	if _, err := New(base, "not-the-token-0123456789").PDF(ctx, "<p>x</p>", nil); !errors.As(err, &apiErr) || apiErr.Code != "unauthorized" {
		t.Errorf("a wrong token: error = %v", err)
	}
}
