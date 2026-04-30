package pattern

import (
	"fmt"
	"regexp"
	"strings"
)

type fieldType struct {
	GrokName string
	Regex    *regexp.Regexp
	Priority int
	NameHint string
}

var fieldTypes = []fieldType{
	{GrokName: "TIMESTAMP_ISO8601", Regex: re(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?$`), Priority: 1, NameHint: "timestamp"},
	{GrokName: "SYSLOGTIMESTAMP", Regex: re(`^(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2} \d{2}:\d{2}:\d{2}$`), Priority: 1, NameHint: "timestamp"},
	{GrokName: "HTTPDATE", Regex: re(`^\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}$`), Priority: 1, NameHint: "timestamp"},
	{GrokName: "UNIXMS", Regex: re(`^\d{13}$`), Priority: 2, NameHint: "timestamp_ms"},
	{GrokName: "UNIX", Regex: re(`^\d{10}$`), Priority: 2, NameHint: "timestamp"},
	{GrokName: "UUID", Regex: re(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`), Priority: 3, NameHint: "id"},
	{GrokName: "TRACEID", Regex: re(`^[0-9a-fA-F]{32}$`), Priority: 3, NameHint: "trace_id"},
	{GrokName: "SPANID", Regex: re(`^[0-9a-fA-F]{16}$`), Priority: 3, NameHint: "span_id"},
	{GrokName: "MAC", Regex: re(`^(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$`), Priority: 4, NameHint: "mac"},
	{GrokName: "IPV4", Regex: re(`^(?:(?:25[0-5]|2[0-4]\d|[01]?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d?\d)$`), Priority: 5, NameHint: "ip"},
	{GrokName: "IPV6", Regex: re(`^(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f]{0,4}$`), Priority: 5, NameHint: "ip"},
	{GrokName: "HOSTPORT", Regex: re(`^[A-Za-z0-9_.:-]+:\d+$`), Priority: 6, NameHint: "endpoint"},
	{GrokName: "EMAILADDRESS", Regex: re(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`), Priority: 7, NameHint: "email"},
	{GrokName: "URI", Regex: re(`^[A-Za-z][A-Za-z0-9+\-.]*://\S+$`), Priority: 8, NameHint: "url"},
	{GrokName: "URIPATHPARAM", Regex: re(`^/[^\s#]*(?:\?[^\s#]*)?$`), Priority: 9, NameHint: "path"},
	{GrokName: "URIPATH", Regex: re(`^/[^\s?#]*$`), Priority: 10, NameHint: "path"},
	{GrokName: "LOGLEVEL", Regex: re(`^(?i:trace|debug|info|notice|warn(?:ing)?|err(?:or)?|crit(?:ical)?|fatal|panic|alert|emerg(?:ency)?|verbose)$`), Priority: 11, NameHint: "level"},
	{GrokName: "DURATION", Regex: re(`^\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)$`), Priority: 12, NameHint: "duration"},
	{GrokName: "BOOLEAN", Regex: re(`^(?i:true|false|yes|no|on|off)$`), Priority: 13, NameHint: "flag"},
	{GrokName: "BASE16NUM", Regex: re(`^(?:0[xX][0-9A-Fa-f]+|[0-9A-Fa-f]*[A-Fa-f][0-9A-Fa-f]*)$`), Priority: 14, NameHint: "hex"},
	{GrokName: "INT", Regex: re(`^[+-]?\d+$`), Priority: 15, NameHint: "n"},
	{GrokName: "FLOAT", Regex: re(`^[+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:[eE][+-]?\d+)?$`), Priority: 16, NameHint: "n"},
	{GrokName: "NUMBER", Regex: re(`^-?\d+(?:\.\d+)?$`), Priority: 17, NameHint: "n"},
	{GrokName: "QUOTEDSTRING", Regex: re(`^"(?:\\.|[^"\\])*"$`), Priority: 18, NameHint: "text"},
	{GrokName: "WORD", Regex: re(`^\w+$`), Priority: 19, NameHint: "word"},
	{GrokName: "NOTSPACE", Regex: re(`^\S+$`), Priority: 20, NameHint: "value"},
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

const slotMatchThreshold = 0.95

func classifySlot(values []string) *fieldType {
	if len(values) == 0 {
		return nil
	}
	for _, ft := range fieldTypes {
		hits := 0
		for _, v := range values {
			if ft.Regex.MatchString(v) {
				hits++
			}
		}
		if float64(hits)/float64(len(values)) >= slotMatchThreshold {
			ft := ft
			return &ft
		}
	}
	// No strict type matched: fall back to NOTSPACE so the slot gets a
	// named capture instead of being rendered as escaped literal text.
	nonEmpty := 0
	for _, v := range values {
		if v != "" {
			nonEmpty++
		}
	}
	if nonEmpty > 0 {
		ft := fieldType{GrokName: "NOTSPACE", Priority: 50, NameHint: "value"}
		return &ft
	}
	return nil
}

type field struct {
	Start    int
	End      int
	GrokType string
	Name     string
}

type slotRange struct {
	SlotIndex  int
	Start, End int
	Values     []string
}

func resolveSlots(c cluster, lines []string) []slotRange {
	if c.SampleLineIdx < 0 || c.SampleLineIdx >= len(lines) {
		return nil
	}
	sample := lines[c.SampleLineIdx]
	sampleTokens := defaultBackend.Tokenize(sample)
	if len(sampleTokens) != c.TokenCount {
		return nil
	}
	spans := tokenSpansOf(sample, sampleTokens)

	var slots []slotRange
	var slotPos []int
	for i, p := range c.Template {
		if !p.IsSlot {
			continue
		}
		if i >= len(spans) {
			return nil
		}
		slots = append(slots, slotRange{
			SlotIndex: i,
			Start:     spans[i].Start,
			End:       spans[i].End,
		})
		slotPos = append(slotPos, i)
	}

	for _, line := range lines {
		toks := defaultBackend.Tokenize(line)
		if len(toks) != c.TokenCount {
			continue
		}
		for si, pos := range slotPos {
			if pos >= len(toks) {
				continue
			}
			slots[si].Values = append(slots[si].Values, toks[pos])
		}
	}
	return slots
}

func autoFieldsFromSlots(slots []slotRange, sample string, c cluster) []field {
	used := make(map[string]int)
	fields := make([]field, 0, len(slots))

	for _, s := range slots {
		ft := classifySlot(s.Values)
		if ft == nil {
			continue
		}
		name := suggestName(c, s, ft)
		if used[name] > 0 {
			name = fmt.Sprintf("%s_%d", name, used[name]+1)
		}
		used[name]++

		fields = append(fields, field{
			Start:    s.Start,
			End:      s.End,
			GrokType: ft.GrokName,
			Name:     name,
		})
	}
	return fields
}

func suggestName(c cluster, s slotRange, ft *fieldType) string {
	if s.SlotIndex > 0 {
		prev := c.Template[s.SlotIndex-1]
		if !prev.IsSlot {
			stripped := strings.TrimRight(prev.Token, ":=")
			name := canonicalName(strings.ToLower(stripped))
			if isValidName(name) && !weakFieldName(name) {
				return name
			}
		}
	}
	return ft.NameHint
}

var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func isValidName(s string) bool { return nameRe.MatchString(strings.ToLower(s)) }

func weakFieldName(s string) bool {
	switch s {
	case "a", "an", "and", "as", "at", "by", "for", "from", "in", "into", "of", "on", "or", "the", "to", "with":
		return true
	}
	// Very short tokens carry too little information to be useful as
	// field names — unless they were already canonicalised into a known
	// alias (e.g. "ts" → "timestamp"), in which case canonicalName has
	// already widened them and they will not appear here.
	if len(s) < 3 {
		return true
	}
	return false
}

var canonicalNames = map[string]string{
	"ts": "timestamp", "time": "timestamp", "timestamp": "timestamp",
	"lvl": "level", "levelname": "level", "severity": "level",
	"msg": "message", "message": "message",
	"logger_name": "logger", "log": "logger",
	"statuscode": "status_code",
	"latency":    "duration",
	"trace":      "trace_id", "traceid": "trace_id",
	"span": "span_id", "spanid": "span_id",
}

func canonicalName(s string) string {
	s = strings.Trim(s, `"'[](){}<>`)
	s = strings.ToLower(strings.ReplaceAll(s, "-", "_"))
	if mapped, ok := canonicalNames[s]; ok {
		return mapped
	}
	return s
}
