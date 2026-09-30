package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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

// segment is one slice of an input line: a token capture, or the literal text
// between captures. A line's segments concatenate to reproduce the line.
type segment struct {
	Text  string `json:"text"`
	Token string `json:"token,omitempty"`
	Field string `json:"field,omitempty"`
}

// linePayload is the per-line rendering model for the UI: whether the line
// matched, and its token segments when the pattern names any fields.
type linePayload struct {
	Matched  bool      `json:"matched"`
	Segments []segment `json:"segments"`
}

type metaPayload struct {
	Lines     int   `json:"lines"`
	ElapsedMs int64 `json:"elapsedMs"`
}

type discoverResponse struct {
	OK      bool           `json:"ok"`
	Pattern patternPayload `json:"pattern"`
	Lines   []linePayload  `json:"lines,omitempty"`
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

// --- token segments ---

// Token segments are only computed within these bounds so a huge paste cannot
// produce a huge JSON payload or DOM.
const (
	maxTokenLines = 2000
	maxTokenBytes = 500000
)

// grokFieldRe mirrors the library's %{NAME}, %{NAME:field}, %{NAME:field:type}.
var grokFieldRe = regexp.MustCompile(`%\{(\w+)(?::([\w.@-]+)(?::(\w+))?)?\}`)

// buildLineTokens maps each input line to its token segments. Returns nil if
// the pattern cannot be compiled (the UI then shows plain text).
func buildLineTokens(grok string, extras map[string]string, lines []string) []linePayload {
	re, err := l2g.CompileGrok(grok, extras)
	if err != nil {
		return nil
	}
	names := re.SubexpNames()
	tokens := groupTokens(grok, names)

	out := make([]linePayload, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = linePayload{Segments: []segment{{Text: ""}}}
			continue
		}
		idx := re.FindStringSubmatchIndex(line)
		if idx == nil {
			out[i] = linePayload{Segments: []segment{{Text: line}}}
			continue
		}
		out[i] = linePayload{Matched: true, Segments: segmentsFor(line, idx, names, tokens)}
	}
	return out
}

// groupTokens pairs each compiled subexpression name with the Grok primitive
// referenced for that field. Duplicate field names are uniquified by the
// compiler (e.g. n, n_2), so per-base queues keep the pairing in order.
func groupTokens(grok string, names []string) []string {
	queues := map[string][]string{}
	for _, m := range grokFieldRe.FindAllStringSubmatch(grok, -1) {
		if m[2] == "" {
			continue
		}
		field := sanitizeField(m[2])
		queues[field] = append(queues[field], m[1])
	}
	tokens := make([]string, len(names))
	for g := 1; g < len(names); g++ {
		name := names[g]
		if name == "" {
			continue
		}
		if q := queues[name]; len(q) > 0 {
			tokens[g] = q[0]
			queues[name] = q[1:]
			continue
		}
		if base := trimNumSuffix(name); base != name {
			if q := queues[base]; len(q) > 0 {
				tokens[g] = q[0]
				queues[base] = q[1:]
			}
		}
	}
	return tokens
}

// segmentsFor walks the match spans and emits a segment per token (outermost
// capture wins) plus literal segments for the text between them.
func segmentsFor(line string, idx []int, names, tokens []string) []segment {
	type span struct {
		start, end int
		token      string
		field      string
	}
	spans := make([]span, 0, len(names))
	for g := 1; g < len(names); g++ {
		start, end := idx[2*g], idx[2*g+1]
		if start < 0 || end <= start {
			continue
		}
		spans = append(spans, span{start: start, end: end, token: tokens[g], field: names[g]})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end // longer (outer) first
	})

	var segs []segment
	pos := 0
	for _, s := range spans {
		if s.start < pos {
			continue // overlaps an already-emitted (outer) span
		}
		if s.start > pos {
			segs = append(segs, segment{Text: line[pos:s.start]})
		}
		segs = append(segs, segment{Text: line[s.start:s.end], Token: s.token, Field: s.field})
		pos = s.end
	}
	if pos < len(line) {
		segs = append(segs, segment{Text: line[pos:]})
	}
	if len(segs) == 0 {
		segs = append(segs, segment{Text: line})
	}
	return segs
}

// trimNumSuffix removes the compiler's duplicate-field suffix (`_2`).
func trimNumSuffix(s string) string {
	i := strings.LastIndexByte(s, '_')
	if i <= 0 || i == len(s)-1 {
		return s
	}
	for _, c := range s[i+1:] {
		if c < '0' || c > '9' {
			return s
		}
	}
	return s[:i]
}

// sanitizeField mirrors the library's field-name sanitization so parsed refs
// match the compiled subexpression names.
func sanitizeField(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = append([]byte{'_'}, out...)
	}
	return string(out)
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

	// Per-line token segments for the UI, aligned 1:1 with the split input so
	// the client can tint each token by the Grok primitive it matched. Bounded
	// so a huge paste does not produce a huge JSON payload or DOM.
	var lineTokens []linePayload
	if len(lines) <= maxTokenLines && len(req.Logs) <= maxTokenBytes {
		lineTokens = buildLineTokens(dp.Grok, dp.CustomPatterns, lines)
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
		Lines: lineTokens,
		Meta: metaPayload{
			Lines:     nonEmptyCount(lines),
			ElapsedMs: time.Since(started).Milliseconds(),
		},
	})
}
