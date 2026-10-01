// Package webapi is the transport-independent core of the log2grok web UI:
// it turns pasted text into a discovery Response (pattern, coverage, and
// per-line token segments). The HTTP server and the in-browser WASM build both
// call Run, so the two front ends cannot drift apart.
package webapi

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	l2g "github.com/logsonic/log2grok/pkg/log2grok"
)

// ErrNoContent is returned by Run when the input has no non-blank line.
var ErrNoContent = errors.New("webapi: no log lines")

// --- response contract (shared by the HTTP server and the WASM build) ---

type PatternPayload struct {
	Grok         string  `json:"grok"`
	Source       string  `json:"source"`
	SourceFamily string  `json:"sourceFamily"`
	Coverage     float64 `json:"coverage"`
	Matched      int     `json:"matched"`
	Total        int     `json:"total"`
	Truncated    bool    `json:"truncated"`
	Estimated    bool    `json:"estimated"`
}

// Segment is one slice of an input line: a token capture, or the literal text
// between captures. A line's segments concatenate to reproduce the line.
type Segment struct {
	Text  string `json:"text"`
	Token string `json:"token,omitempty"`
	Field string `json:"field,omitempty"`
}

// LinePayload is the per-line rendering model for the UI: whether the line
// matched, and its token segments when the pattern names any fields.
type LinePayload struct {
	Matched  bool      `json:"matched"`
	Segments []Segment `json:"segments"`
}

type MetaPayload struct {
	Lines     int   `json:"lines"`
	ElapsedMs int64 `json:"elapsedMs"`
}

// Response is the success half of the wire contract.
type Response struct {
	OK      bool           `json:"ok"`
	Pattern PatternPayload `json:"pattern"`
	Lines   []LinePayload  `json:"lines,omitempty"`
	Meta    MetaPayload    `json:"meta"`
}

// APIError and ErrorResponse are the failure half of the wire contract.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	OK    bool     `json:"ok"`
	Error APIError `json:"error"`
}

// Fail maps a Run error to its wire form: the HTTP status the server should
// use, plus the error body. The WASM build ignores the status.
func Fail(err error) (int, ErrorResponse) {
	if errors.Is(err, ErrNoContent) {
		return 400, ErrorResponse{Error: APIError{Code: "empty_input", Message: "Paste at least one log line."}}
	}
	return 500, ErrorResponse{Error: APIError{Code: "internal", Message: "Discovery failed: " + err.Error()}}
}

// --- helpers ---

// SplitLines splits pasted text into lines, stripping a single trailing CR
// from CRLF input and dropping trailing empty lines (a final newline).
// Interior blank lines are preserved; the library discards them.
func SplitLines(s string) []string {
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

// HasContent reports whether any line has non-whitespace content.
func HasContent(lines []string) bool {
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

// --- token segments ---

// Token segments are only computed within these bounds so a huge paste cannot
// produce a huge JSON payload or DOM.
const (
	MaxTokenLines = 2000
	MaxTokenBytes = 500000
)

// grokFieldRe mirrors the library's %{NAME}, %{NAME:field}, %{NAME:field:type}.
var grokFieldRe = regexp.MustCompile(`%\{(\w+)(?::([\w.@-]+)(?::(\w+))?)?\}`)

// BuildLineTokens maps each input line to its token segments. Returns nil if
// the pattern cannot be compiled (the UI then shows plain text).
func BuildLineTokens(grok string, extras map[string]string, lines []string) []LinePayload {
	re, err := l2g.CompileGrok(grok, extras)
	if err != nil {
		return nil
	}
	names := re.SubexpNames()
	tokens := groupTokens(grok, names)

	out := make([]LinePayload, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = LinePayload{Segments: []Segment{{Text: ""}}}
			continue
		}
		idx := re.FindStringSubmatchIndex(line)
		if idx == nil {
			out[i] = LinePayload{Segments: []Segment{{Text: line}}}
			continue
		}
		out[i] = LinePayload{Matched: true, Segments: segmentsFor(line, idx, names, tokens)}
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

// segmentsFor walks the match spans and emits a Segment per token (outermost
// capture wins) plus literal segments for the text between them.
func segmentsFor(line string, idx []int, names, tokens []string) []Segment {
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

	var segs []Segment
	pos := 0
	for _, s := range spans {
		if s.start < pos {
			continue // overlaps an already-emitted (outer) span
		}
		if s.start > pos {
			segs = append(segs, Segment{Text: line[pos:s.start]})
		}
		segs = append(segs, Segment{Text: line[s.start:s.end], Token: s.token, Field: s.field})
		pos = s.end
	}
	if pos < len(line) {
		segs = append(segs, Segment{Text: line[pos:]})
	}
	if len(segs) == 0 {
		segs = append(segs, Segment{Text: line})
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

// Run discovers a Grok pattern for the pasted text and builds the UI response.
// ErrNoContent signals input with no usable line; any other error is a
// discovery failure.
func Run(logs string) (*Response, error) {
	lines := SplitLines(logs)
	if !HasContent(lines) {
		return nil, ErrNoContent
	}

	started := time.Now()
	dp, err := l2g.Discover(lines, l2g.Options{})
	if err != nil {
		if errors.Is(err, l2g.ErrEmptyInput) {
			return nil, ErrNoContent
		}
		return nil, err
	}

	// Per-line token segments for the UI, aligned 1:1 with the split input so
	// the client can tint each token by the Grok primitive it matched. Bounded
	// so a huge paste does not produce a huge JSON payload or DOM.
	var lineTokens []LinePayload
	if len(lines) <= MaxTokenLines && len(logs) <= MaxTokenBytes {
		lineTokens = BuildLineTokens(dp.Grok, dp.CustomPatterns, lines)
	}

	return &Response{
		OK: true,
		Pattern: PatternPayload{
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
		Meta: MetaPayload{
			Lines:     nonEmptyCount(lines),
			ElapsedMs: time.Since(started).Milliseconds(),
		},
	}, nil
}
