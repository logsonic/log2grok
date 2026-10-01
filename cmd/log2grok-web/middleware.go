package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// assetVersion is a short content hash of every embedded static file. It is
// appended to asset URLs (?v=...) so they can be cached as immutable and still
// change the moment a file does.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := staticFS.ReadFile(path)
		io.WriteString(h, path)
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}()

// The page loads only same-origin scripts and styles; the favicon is a data:
// URI. No inline script or style is needed, so the policy can stay strict.
// 'wasm-unsafe-eval' lets an in-browser WASM engine compile if one is served.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// secure adds baseline security headers and cache policy to every response:
// fingerprinted assets are immutable, everything else must revalidate, and API
// responses (which echo user input) are never stored.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			h.Set("Cache-Control", "no-store")
		case strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Query().Get("v") != "":
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			h.Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

var gzPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	started bool
	enabled bool
}

func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "application/json") ||
		strings.HasPrefix(ct, "application/javascript") || strings.HasPrefix(ct, "image/svg")
}

func (g *gzipWriter) start() {
	if g.started {
		return
	}
	g.started = true
	h := g.Header()
	if h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
		g.enabled = true
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		g.zw = gzPool.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
	}
}

func (g *gzipWriter) WriteHeader(code int) {
	g.start()
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.started {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.start()
	}
	if g.enabled {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipWriter) close() {
	if g.enabled {
		_ = g.zw.Close()
		gzPool.Put(g.zw)
	}
}

// gzipped compresses text responses for clients that accept gzip. The log
// payloads and the JSON that echoes them compress well; the Vary header keeps
// shared caches honest.
func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
