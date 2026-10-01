package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/logsonic/log2grok/internal/webapi"
)

//go:embed static
var staticFS embed.FS

// newMux builds every route. maxBody bounds the /api/discover request body.
func newMux(maxBody int64) *http.ServeMux {
	sub, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/discover", func(w http.ResponseWriter, r *http.Request) {
		handleDiscover(w, r, maxBody)
	})
	mux.HandleFunc("/", handleIndex)
	return mux
}

// newHandler is the production handler: routes wrapped with security headers
// and gzip.
func newHandler(maxBody int64) http.Handler {
	return secure(gzipped(newMux(maxBody)))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		serveStaticPage(w, "static/404.html", http.StatusNotFound)
		return
	}
	serveStaticPage(w, "static/index.html", http.StatusOK)
}

// serveStaticPage serves an embedded HTML page, stamping the asset version
// into its {{v}} placeholders.
func serveStaticPage(w http.ResponseWriter, name string, status int) {
	data, err := staticFS.ReadFile(name)
	if err != nil {
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	page := strings.ReplaceAll(string(data), "{{v}}", assetVersion)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, page)
}

// --- request contract ---

type discoverRequest struct {
	Logs string `json:"logs"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, webapi.ErrorResponse{Error: webapi.APIError{Code: code, Message: message}})
}

// --- handler ---

func handleDiscover(w http.ResponseWriter, r *http.Request, maxBody int64) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST for this endpoint.")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Input is too large; paste at most 8 MiB.")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	// encoding/json coerces invalid UTF-8 to the replacement character rather
	// than failing, so validate the raw bytes explicitly (design spec §5).
	if !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, "bad_request", "Input is not valid UTF-8.")
		return
	}

	var req discoverRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `Send JSON like {"logs": "..."}.`)
		return
	}

	resp, err := webapi.Run(req.Logs)
	if err != nil {
		status, body := webapi.Fail(err)
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
