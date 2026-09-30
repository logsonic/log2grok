package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
