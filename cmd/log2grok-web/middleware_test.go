package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func prodGet(t *testing.T, path string, gz bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if gz {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	rec := httptest.NewRecorder()
	newHandler(testMaxBody).ServeHTTP(rec, req)
	return rec
}

func TestSecurityHeaders(t *testing.T) {
	h := prodGet(t, "/", false).Header()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("unexpected CSP %q", csp)
	}
}

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	body := prodGet(t, "/", false).Body.String()
	if strings.Contains(body, "{{v}}") || !strings.Contains(body, "/static/app.js?v="+assetVersion) {
		t.Fatalf("index does not carry the asset version %q", assetVersion)
	}
	if cc := prodGet(t, "/static/app.js?v="+assetVersion, false).Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset Cache-Control = %q", cc)
	}
	if cc := prodGet(t, "/", false).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}
}

func TestGzipNegotiation(t *testing.T) {
	rec := prodGet(t, "/static/styles.css", true)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("styles.css not gzipped for an accepting client")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.Contains(string(plain), "--accent") {
		t.Error("gunzipped body is not the stylesheet")
	}
	if prodGet(t, "/static/styles.css", false).Header().Get("Content-Encoding") != "" {
		t.Error("gzip applied without Accept-Encoding")
	}
}

func TestNotFoundPage(t *testing.T) {
	rec := prodGet(t, "/nope", false)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No pattern matches") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAPINotCached(t *testing.T) {
	if cc := prodGet(t, "/api/discover", false).Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("api Cache-Control = %q", cc)
	}
}

// The standalone Cloudflare site must ship the same CSP as the Go server.
func TestCloudflareHeadersMatchServerCSP(t *testing.T) {
	data, err := os.ReadFile("../../deploy/cloudflare/_headers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Content-Security-Policy: "+contentSecurityPolicy+"\n") {
		t.Error("deploy/cloudflare/_headers CSP differs from contentSecurityPolicy")
	}
}
