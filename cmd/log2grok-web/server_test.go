package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const testMaxBody = int64(8 << 20)

func testMux() *http.ServeMux { return newMux(testMaxBody) }

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	testMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postJSON(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestIndexServed(t *testing.T) {
	rec := get(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "log2grok") {
		t.Fatal("index body does not mention log2grok")
	}
}

func TestUnknownPathsReturn404(t *testing.T) {
	for _, path := range []string{"/nope", "/static/nope.js"} {
		rec := get(t, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "one", []string{"one"}},
		{"trailing newline", "one\n", []string{"one"}},
		{"crlf", "one\r\ntwo\r\n", []string{"one", "two"}},
		{"interior blank kept", "one\n\ntwo", []string{"one", "", "two"}},
		{"only newlines", "\n\n", nil},
	}
	for _, tc := range cases {
		if got := splitLines(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: splitLines(%q) = %#v, want %#v", tc.name, tc.in, got, tc.want)
		}
	}
}

const nginxSample = `10.0.0.1 - alice [15/Jan/2025:10:23:45 +0000] "GET /index.html HTTP/1.1" 200 1024
10.0.0.2 - bob [15/Jan/2025:10:23:46 +0000] "POST /api HTTP/1.1" 201 0
10.0.0.3 - carol [15/Jan/2025:10:23:47 +0000] "GET /health HTTP/1.1" 200 3`

func TestDiscoverSuccess(t *testing.T) {
	rec := postJSON(t, testMux(), `{"logs":`+jsonString(nginxSample)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK      bool `json:"ok"`
		Pattern struct {
			Grok     string  `json:"grok"`
			Source   string  `json:"source"`
			Total    int     `json:"total"`
			Matched  int     `json:"matched"`
			Coverage float64 `json:"coverage"`
		} `json:"pattern"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if !resp.OK {
		t.Fatalf("ok = false; body=%s", rec.Body.String())
	}
	if resp.Pattern.Grok == "" {
		t.Fatal("pattern.grok is empty")
	}
	if resp.Pattern.Source == "" {
		t.Fatal("pattern.source is empty")
	}
	if resp.Pattern.Total != 3 {
		t.Fatalf("pattern.total = %d, want 3", resp.Pattern.Total)
	}
	if resp.Pattern.Matched == 0 {
		t.Fatal("pattern.matched = 0, want > 0")
	}
	if resp.Pattern.Coverage <= 0 || resp.Pattern.Coverage > 1 {
		t.Fatalf("pattern.coverage = %v, want (0,1]", resp.Pattern.Coverage)
	}
}

// jsonString encodes s as a JSON string literal for building request bodies.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDiscoverMixedInputReturnsValidJSON(t *testing.T) {
	mixed := nginxSample + "\n" +
		`{"ts":"2025-01-15T10:23:45Z","level":"info","msg":"server started"}` + "\n" +
		`{"ts":"2025-01-15T10:23:46Z","level":"warn","msg":"slow request"}`
	rec := postJSON(t, testMux(), `{"logs":`+jsonString(mixed)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mixed input produced invalid JSON: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("ok = %v, want true", resp["ok"])
	}
}

func TestDiscoverLongSingleLine(t *testing.T) {
	long := strings.Repeat("x", 200000)
	rec := postJSON(t, testMux(), `{"logs":`+jsonString(long)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) (int, string, string) {
	t.Helper()
	var resp struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error response is not valid JSON: %v; body=%s", err, rec.Body.String())
	}
	return rec.Code, resp.Error.Code, resp.Error.Message
}

func TestDiscoverEmptyInput(t *testing.T) {
	for _, body := range []string{`{"logs":""}`, `{"logs":"   \n  \n\t"}`, `{}`} {
		rec := postJSON(t, testMux(), body)
		status, code, _ := decodeErr(t, rec)
		if status != http.StatusBadRequest || code != "empty_input" {
			t.Fatalf("body %s: got %d/%s, want 400/empty_input", body, status, code)
		}
	}
}

func TestDiscoverMalformedJSON(t *testing.T) {
	rec := postJSON(t, testMux(), `{"logs":`)
	status, code, _ := decodeErr(t, rec)
	if status != http.StatusBadRequest || code != "bad_request" {
		t.Fatalf("got %d/%s, want 400/bad_request", status, code)
	}
}

func TestDiscoverWrongMethod(t *testing.T) {
	rec := get(t, "/api/discover")
	status, code, _ := decodeErr(t, rec)
	if status != http.StatusMethodNotAllowed || code != "method_not_allowed" {
		t.Fatalf("got %d/%s, want 405/method_not_allowed", status, code)
	}
}

func TestDiscoverTooLarge(t *testing.T) {
	mux := newMux(64) // tiny cap
	rec := postJSON(t, mux, `{"logs":"`+strings.Repeat("a", 200)+`"}`)
	status, code, _ := decodeErr(t, rec)
	if status != http.StatusRequestEntityTooLarge || code != "too_large" {
		t.Fatalf("got %d/%s, want 413/too_large", status, code)
	}
}

func TestDiscoverInvalidUTF8(t *testing.T) {
	// Raw invalid UTF-8 must be rejected cleanly, never panic.
	req := httptest.NewRequest(http.MethodPost, "/api/discover",
		strings.NewReader("{\"logs\":\"\xff\xfe\"}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	testMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDiscoverConcurrent(t *testing.T) {
	mux := testMux()
	body := `{"logs":` + jsonString(nginxSample) + `}`

	rec := postJSON(t, mux, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("baseline status = %d", rec.Code)
	}
	var base struct {
		Pattern struct {
			Grok string `json:"grok"`
		} `json:"pattern"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &base)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := postJSON(t, mux, body)
			if r.Code != http.StatusOK {
				t.Errorf("concurrent status = %d", r.Code)
				return
			}
			var got struct {
				Pattern struct {
					Grok string `json:"grok"`
				} `json:"pattern"`
			}
			_ = json.Unmarshal(r.Body.Bytes(), &got)
			if got.Pattern.Grok != base.Pattern.Grok {
				t.Errorf("concurrent grok = %q, want %q", got.Pattern.Grok, base.Pattern.Grok)
			}
		}()
	}
	wg.Wait()
}

func TestIndexMarkup(t *testing.T) {
	body := get(t, "/").Body.String()
	for _, needle := range []string{
		`id="logs"`, `id="discover"`, `id="lineCount"`, `id="result"`,
		`id="feedback"`, `id="copy"`, `/static/styles.css`, `/static/app.js`,
		`id="result" class="result" role="status" aria-live="polite"`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("index is missing %s", needle)
		}
	}
}

func TestStylesServed(t *testing.T) {
	rec := get(t, "/static/styles.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", ct)
	}
	body := rec.Body.String()
	for _, needle := range []string{"--accent", "prefers-color-scheme", "prefers-reduced-motion"} {
		if !strings.Contains(body, needle) {
			t.Errorf("styles.css is missing %q", needle)
		}
	}
}

func TestAppServed(t *testing.T) {
	rec := get(t, "/static/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, needle := range []string{"/api/discover", "data.error", "0.5", "EXAMPLES"} {
		if !strings.Contains(body, needle) {
			t.Errorf("app.js is missing %q", needle)
		}
	}
}
