package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "0123456789abcdef"

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("fine")) })
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not a JSON error: %q", rec.Body.String())
	}
	return body["code"]
}

func TestBearerAuth(t *testing.T) {
	h := BearerAuth(token)(ok())
	for name, tc := range map[string]struct {
		header string
		want   int
	}{
		"valid":                {"Bearer " + token, http.StatusOK},
		"lowercase scheme":     {"bearer " + token, http.StatusOK},
		"missing":              {"", http.StatusUnauthorized},
		"wrong token":          {"Bearer " + token + "x", http.StatusUnauthorized},
		"shorter token":        {"Bearer 0123", http.StatusUnauthorized},
		"wrong scheme":         {"Basic " + token, http.StatusUnauthorized},
		"token without scheme": {token, http.StatusUnauthorized},
		"empty bearer":         {"Bearer ", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/pdf", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.want)
		}
		if tc.want == http.StatusUnauthorized {
			if errorCode(t, rec) != "unauthorized" || rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s: want an unauthorized JSON error with WWW-Authenticate", name)
			}
		}
	}
}

func TestRecoverHidesThePanicValue(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret-value") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret-value") {
		t.Error("the panic value leaked to the client")
	}
	if !strings.Contains(logs.String(), "panic serving request") {
		t.Error("the panic was not logged")
	}
}

func TestRequestLogFieldsAndNoContent(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := RequestLog(logger)(ok())

	body := strings.NewReader(`{"html":"PRIVATE-BIRTH-DATA"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/pdf?secret=QUERY-SECRET", body)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := logs.String()
	for _, want := range []string{`"method":"POST"`, `"path":"/v1/pdf"`, `"status":200`, `"duration_ms"`, `"response_bytes":4`, `"request_id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log %s is missing %s", out, want)
		}
	}
	for _, leaked := range []string{"PRIVATE-BIRTH-DATA", "QUERY-SECRET", token} {
		if strings.Contains(out, leaked) {
			t.Errorf("the log contains %q", leaked)
		}
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("no request id was echoed")
	}
}

func TestRequestIDIsKeptWhenValidAndReplacedWhenNot(t *testing.T) {
	h := RequestLog(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))(ok())
	for name, tc := range map[string]struct{ in, want string }{
		"valid":    {"trace-123", "trace-123"},
		"too long": {strings.Repeat("a", 65), ""},
		"unsafe":   {"bad id\nnext", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-Id", tc.in)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-Id")
		if tc.want != "" && got != tc.want {
			t.Errorf("%s: id = %q, want %q", name, got, tc.want)
		}
		if tc.want == "" && (got == tc.in || len(got) != 16) {
			t.Errorf("%s: id = %q, want a fresh 16-character id", name, got)
		}
	}
}
