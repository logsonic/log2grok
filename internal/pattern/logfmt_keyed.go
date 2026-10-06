package pattern

import (
	"fmt"
	"regexp"
	"strings"
)

// logfmtPair is one key=value pair from a logfmt line. Quote is the byte
// that wrapped Value ('"' or '\”), or 0 for a bare value.
type logfmtPair struct {
	Key   string
	Value string
	Quote byte
}

func isLogfmtKeyByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case c >= '0' && c <= '9', c == '.', c == '-':
		return !first
	}
	return false
}

// parseLogfmtPairs splits a line into key=value pairs separated by exactly
// one space. Anything else (bare words, double spaces, empty values,
// unterminated quotes) reports false so the caller keeps the blob.
func parseLogfmtPairs(line string) ([]logfmtPair, bool) {
	line = strings.TrimSuffix(line, "\r")
	var pairs []logfmtPair
	i := 0
	for i < len(line) {
		start := i
		for i < len(line) && isLogfmtKeyByte(line[i], i == start) {
			i++
		}
		if i == start || i >= len(line) || line[i] != '=' {
			return nil, false
		}
		p := logfmtPair{Key: line[start:i]}
		i++ // skip '='
		if i >= len(line) {
			return nil, false
		}
		switch q := line[i]; q {
		case '"', '\'':
			j := i + 1
			for j < len(line) && line[j] != q {
				if line[j] == '\\' && q == '"' {
					j++
				}
				j++
			}
			if j >= len(line) {
				return nil, false
			}
			p.Value, p.Quote = line[i+1:j], q
			i = j + 1
		default:
			j := i
			for j < len(line) && line[j] != ' ' {
				j++
			}
			if j == i {
				return nil, false
			}
			p.Value = line[i:j]
			i = j
		}
		pairs = append(pairs, p)
		if i == len(line) {
			break
		}
		if line[i] != ' ' || i+1 >= len(line) || line[i+1] == ' ' {
			return nil, false
		}
		i++ // skip the single space separator
	}
	return pairs, len(pairs) > 0
}

// logfmtFieldName turns a key into an RE2-safe capture name (dots and
// dashes become '_', ts→timestamp, msg→message) and dedupes with _2, _3.
func logfmtFieldName(key string, used map[string]int) string {
	base := sanitizeFieldName(canonicalName(key))
	if base == "" {
		base = "field"
	}
	name := base
	for n := 2; used[name] > 0; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	used[name]++
	return name
}

// logfmtCapture types one key's column by population vote.
func logfmtCapture(col []logfmtPair, name string) string {
	quote := col[0].Quote
	values := make([]string, 0, len(col))
	for _, p := range col {
		if p.Quote != quote {
			return "%{DATA:" + name + "}"
		}
		values = append(values, p.Value)
	}
	switch quote {
	case '"':
		return "%{QUOTEDSTRING:" + name + "}"
	case '\'':
		return "'%{DATA:" + name + "}'"
	}
	prim := voteSingle(values)
	if prim != nil && prim.GrokName != "NOTSPACE" {
		return "%{" + prim.GrokName + ":" + name + "}"
	}
	return "%{NOTSPACE:" + name + "}"
}

// renderLogfmtKeyed emits `key=%{TYPE:name}` per key when every sample line
// parses fully and carries the same key sequence, and the result matches
// every sample line. ok=false means: keep the GREEDYDATA blob.
func renderLogfmtKeyed(sample []string) (string, bool) {
	if len(sample) < 2 {
		return "", false
	}
	var keys []string
	var cols [][]logfmtPair
	for _, line := range sample {
		pairs, ok := parseLogfmtPairs(line)
		if !ok {
			return "", false
		}
		if keys == nil {
			for _, p := range pairs {
				keys = append(keys, p.Key)
			}
			cols = make([][]logfmtPair, len(keys))
		}
		if len(pairs) != len(keys) {
			return "", false
		}
		for j, p := range pairs {
			if p.Key != keys[j] {
				return "", false
			}
			cols[j] = append(cols[j], p)
		}
	}
	used := make(map[string]int)
	parts := make([]string, len(keys))
	for j, key := range keys {
		parts[j] = regexp.QuoteMeta(key) + "=" + logfmtCapture(cols[j], logfmtFieldName(key, used))
	}
	grok := strings.Join(parts, " ")
	re, err := CompileGrok(grok, nil)
	if err != nil {
		return "", false
	}
	for _, line := range sample {
		if !re.MatchString(line) {
			return "", false
		}
	}
	return grok, true
}
