package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	l2g "github.com/logsonic/log2grok/pkg/log2grok"
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

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// --- request/response contract ---

type discoverRequest struct {
	Logs string `json:"logs"`
}

type patternPayload struct {
	Grok         string  `json:"grok"`
	Source       string  `json:"source"`
	SourceFamily string  `json:"sourceFamily"`
	Coverage     float64 `json:"coverage"`
	Matched      int     `json:"matched"`
	Total        int     `json:"total"`
	Truncated    bool    `json:"truncated"`
	Estimated    bool    `json:"estimated"`
}

type metaPayload struct {
	Lines     int   `json:"lines"`
	ElapsedMs int64 `json:"elapsedMs"`
}

type discoverResponse struct {
	OK      bool           `json:"ok"`
	Pattern patternPayload `json:"pattern"`
	Meta    metaPayload    `json:"meta"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	OK    bool     `json:"ok"`
	Error apiError `json:"error"`
}

// --- helpers ---

// splitLines splits pasted text into lines, stripping a single trailing CR
// from CRLF input and dropping trailing empty lines (a final newline).
// Interior blank lines are preserved; the library discards them.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	for i := range raw {
		raw[i] = strings.TrimSuffix(raw[i], "\r")
	}
	for len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// hasContent reports whether any line has non-whitespace content.
func hasContent(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// nonEmptyCount matches the library's normalization: only "" is dropped.
func nonEmptyCount(lines []string) int {
	n := 0
	for _, line := range lines {
		if line != "" {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{OK: false, Error: apiError{Code: code, Message: message}})
}

func classifyDiscoverError(err error) (int, string, string) {
	if errors.Is(err, l2g.ErrEmptyInput) {
		return http.StatusBadRequest, "empty_input", "Paste at least one log line."
	}
	return http.StatusInternalServerError, "internal", "Discovery failed: " + err.Error()
}

// --- handler ---

func handleDiscover(w http.ResponseWriter, r *http.Request, maxBody int64) {
	var req discoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `Send JSON like {"logs": "..."}.`)
		return
	}

	lines := splitLines(req.Logs)
	if !hasContent(lines) {
		writeError(w, http.StatusBadRequest, "empty_input", "Paste at least one log line.")
		return
	}

	started := time.Now()
	dp, err := l2g.Discover(lines, l2g.Options{})
	if err != nil {
		status, code, msg := classifyDiscoverError(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, discoverResponse{
		OK: true,
		Pattern: patternPayload{
			Grok:         dp.Grok,
			Source:       dp.Source,
			SourceFamily: dp.SourceFamily,
			Coverage:     dp.Coverage,
			Matched:      dp.MatchedCount,
			Total:        dp.TotalLines,
			Truncated:    dp.Truncated,
			Estimated:    dp.Estimated,
		},
		Meta: metaPayload{
			Lines:     nonEmptyCount(lines),
			ElapsedMs: time.Since(started).Milliseconds(),
		},
	})
}
