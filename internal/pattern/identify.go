package pattern

import (
	"encoding/csv"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

type structuredProbe struct {
	Name   string
	Likely func(sample []string) bool
	Render func(sample []string) (grok string, source string, ok bool)
}

var structuredProbes = []structuredProbe{
	dockerJSONProbe,
	pinoJSONProbe,
	bunyanJSONProbe,
	zapJSONProbe,
	ecsJSONProbe,
	cloudTrailJSONProbe,
	suricataEVEProbe,
	kubernetesAuditJSONProbe,
	auditdJSONProbe,
	vaultAuditJSONProbe,
	jsonProbe,
	logfmtProbe,
	cefProbe,
	leefProbe,
	w3cIISProbe,
	androidLogcatProbe,
	healthAppProbe,
	sparkProbe,
	windowsCBSProbe,
	csvProbe,
	tsvProbe,
}

// ---------- helpers ----------

func parseJSONLine(line string) (map[string]any, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return nil, false
	}
	return m, true
}

func jsonObjectFraction(sample []string) float64 {
	if len(sample) == 0 {
		return 0
	}
	hits := 0
	for _, line := range sample {
		if _, ok := parseJSONLine(line); ok {
			hits++
		}
	}
	return float64(hits) / float64(len(sample))
}

func keyFreq(sample []string) (count int, freq map[string]int) {
	freq = make(map[string]int)
	for _, line := range sample {
		obj, ok := parseJSONLine(line)
		if !ok {
			continue
		}
		count++
		for k := range obj {
			freq[k]++
		}
	}
	return count, freq
}

func hasKeysAtFraction(freq map[string]int, total int, frac float64, keys ...string) bool {
	if total == 0 {
		return false
	}
	threshold := float64(total) * frac
	for _, k := range keys {
		if float64(freq[k]) < threshold {
			return false
		}
	}
	return true
}

type jsonShape struct {
	Count     int
	KeyFreq   map[string]int
	TypeFreq  map[string]map[string]int
	FirstKeys []string
}

func observeJSONShape(sample []string) jsonShape {
	shape := jsonShape{
		KeyFreq:  make(map[string]int),
		TypeFreq: make(map[string]map[string]int),
	}
	for _, line := range sample {
		obj, ok := parseJSONLine(line)
		if !ok {
			continue
		}
		shape.Count++
		if len(shape.FirstKeys) == 0 {
			shape.FirstKeys = orderedJSONKeys(strings.TrimSpace(line))
		}
		for key, value := range obj {
			shape.KeyFreq[key]++
			if shape.TypeFreq[key] == nil {
				shape.TypeFreq[key] = make(map[string]int)
			}
			shape.TypeFreq[key][jsonValueKind(value)]++
		}
	}
	return shape
}

func orderedJSONKeys(line string) []string {
	dec := json.NewDecoder(strings.NewReader(line))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil
	}
	keys := make([]string, 0, 16)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, ok := tok.(string)
		if !ok {
			return keys
		}
		keys = append(keys, key)
		var discard any
		if err := dec.Decode(&discard); err != nil {
			return keys
		}
	}
	return keys
}

func commonJSONKeys(shape jsonShape, minFreq float64) []string {
	if shape.Count == 0 {
		return nil
	}
	common := make(map[string]bool)
	for key, count := range shape.KeyFreq {
		if float64(count)/float64(shape.Count) >= minFreq {
			common[key] = true
		}
	}
	out := make([]string, 0, len(common))
	seen := make(map[string]bool, len(common))
	for _, key := range shape.FirstKeys {
		if common[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0, len(common)-len(out))
	for key := range common {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func jsonValueKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "complex"
	}
}

func dominantJSONKind(shape jsonShape, key string) string {
	bestKind, bestCount := "string", 0
	for kind, count := range shape.TypeFreq[key] {
		if count > bestCount {
			bestKind, bestCount = kind, count
		}
	}
	return bestKind
}

func renderJSONSkeleton(sample []string, source string) (string, string, bool) {
	shape := observeJSONShape(sample)
	keys := commonJSONKeys(shape, 0.80)
	if len(keys) == 0 {
		return `\{%{GREEDYDATA:json}\}`, source, true
	}
	common := make(map[string]bool, len(keys))
	for _, key := range keys {
		common[key] = true
	}

	if jsonKeyOrderIsStable(sample, keys) {
		return renderJSONSkeletonOrdered(sample, shape, keys, common, source)
	}
	return renderJSONSkeletonUnordered(shape, keys, source)
}

// renderJSONSkeletonOrdered emits the legacy, order-locked shape: keys
// must appear in their first-line order, separated by literal commas.
// This produces the tightest pattern when producers are deterministic.
func renderJSONSkeletonOrdered(sample []string, shape jsonShape, keys []string, common map[string]bool, source string) (string, string, bool) {
	hasExtra := false
	for key := range shape.KeyFreq {
		if !common[key] {
			hasExtra = true
			break
		}
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		field := canonicalJSONFieldName(key)
		parts = append(parts, `"`+regexp.QuoteMeta(key)+`"\s*:\s*`+jsonValueGrok(field, dominantJSONKind(shape, key)))
	}
	grok := `\{\s*` + strings.Join(parts, `\s*,\s*`)
	if hasExtra {
		grok += `(?:\s*,\s*%{GREEDYDATA:json_extra})?`
	}
	grok += `\s*\}`
	_ = sample
	return grok, source, true
}

// renderJSONSkeletonUnordered emits an order-tolerant shape: each key is
// matched at any position inside the JSON object, with arbitrary other
// `"k":v` pairs allowed before/between/after. Used when sample lines do
// not all agree on key order (e.g. concatenated rotated logs from
// different producers).
//
// The match for each key uses a non-greedy bridge `(?:[^{}]*?,\s*)?` so
// it can skip preceding pairs without devouring the closing brace. We
// require all common keys to appear, in any order, and treat anything
// else as opaque body matched by `[^{}]*` segments.
func renderJSONSkeletonUnordered(shape jsonShape, keys []string, source string) (string, string, bool) {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		field := canonicalJSONFieldName(key)
		// `(?:[^{}]*?,\s*)?` — optional non-greedy bridge of non-brace
		// characters terminated by a comma. Keeps matches inside the
		// current object and lets the key appear at any position.
		parts = append(parts,
			`(?:[^{}]*?,\s*)?"`+regexp.QuoteMeta(key)+`"\s*:\s*`+
				jsonValueGrok(field, dominantJSONKind(shape, key)))
	}
	grok := `\{\s*` + strings.Join(parts, ``) + `[^{}]*\}`
	return grok, source, true
}

// jsonKeyOrderIsStable returns true when every JSON line in sample emits
// the targeted keys in the same relative order (extra keys interleaved
// are allowed). When false, the renderer should produce an order-tolerant
// pattern instead of an order-locked one.
func jsonKeyOrderIsStable(sample []string, keys []string) bool {
	target := make(map[string]int, len(keys))
	for i, k := range keys {
		target[k] = i
	}
	for _, line := range sample {
		ordered := orderedJSONKeys(strings.TrimSpace(line))
		if len(ordered) == 0 {
			continue
		}
		last := -1
		for _, k := range ordered {
			idx, ok := target[k]
			if !ok {
				continue
			}
			if idx < last {
				return false
			}
			last = idx
		}
	}
	return true
}

func canonicalJSONFieldName(key string) string {
	switch key {
	case "@timestamp", "time", "timestamp", "ts", "eventTime":
		return "timestamp"
	case "level", "severity", "log.level", "levelname":
		return "level"
	case "message", "msg", "log":
		return "message"
	case "logger", "logger_name", "name":
		return "logger"
	case "trace_id", "traceId", "traceid", "trace.id":
		return "trace_id"
	case "span_id", "spanId", "spanid", "span.id":
		return "span_id"
	case "request_id", "requestId", "requestid", "request.id":
		return "request_id"
	case "client_ip", "clientIp", "clientip", "src_ip", "source.ip":
		return "client_ip"
	case "method", "http_method", "httpMethod", "verb", "http.request.method":
		return "method"
	case "path", "url", "uri", "request_uri", "http.target":
		return "path"
	case "status", "status_code", "statusCode", "http.response.status_code":
		return "status"
	case "duration", "latency", "response_time", "responseTime", "dur", "event.duration":
		return "duration"
	}
	repl := strings.NewReplacer(".", "_", "-", "_", "@", "", "/", "_")
	name := canonicalName(repl.Replace(key))
	if !isValidName(name) {
		// Replace any remaining non-alphanumeric/underscore characters
		// with underscores, then collapse runs and trim. This keeps key
		// information visible for keys with characters not in the
		// targeted replacer above (spaces, colons, etc.).
		name = sanitizeJSONField(key)
	}
	if !isValidName(name) {
		name = "field_" + strings.TrimLeft(name, "_")
	}
	if !isValidName(name) {
		return "field"
	}
	return name
}

func sanitizeJSONField(key string) string {
	out := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		default:
			if len(out) > 0 && out[len(out)-1] != '_' {
				out = append(out, '_')
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = append([]byte{'_'}, out...)
	}
	return strings.ToLower(string(out))
}

func jsonValueGrok(field, kind string) string {
	switch kind {
	case "number":
		return "%{NUMBER:" + field + "}"
	case "bool":
		return "%{BOOLEAN:" + field + "}"
	case "null", "complex":
		return "%{DATA:" + field + "}"
	default:
		return "%{QUOTEDSTRING:" + field + "}"
	}
}

// ---------- generic JSON probe ----------

var jsonProbe = structuredProbe{
	Name:   "JSON Object",
	Likely: func(sample []string) bool { return jsonObjectFraction(sample) >= 0.90 },
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:JSON Object")
	},
}

// ---------- Docker JSON ----------

var dockerJSONProbe = structuredProbe{
	Name: "Docker JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		return total > 0 && hasKeysAtFraction(freq, total, 0.80, "log", "stream", "time")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Docker JSON")
	},
}

// ---------- Pino ----------

var pinoJSONProbe = structuredProbe{
	Name: "Pino JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		return total > 0 && hasKeysAtFraction(freq, total, 0.80, "level", "time", "msg")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Pino JSON")
	},
}

// ---------- Bunyan ----------

var bunyanJSONProbe = structuredProbe{
	Name: "Bunyan JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		return total > 0 && hasKeysAtFraction(freq, total, 0.80, "name", "level", "time", "msg")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Bunyan JSON")
	},
}

// ---------- Zap ----------

var zapJSONProbe = structuredProbe{
	Name: "Zap JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		hasTS := freq["ts"] > 0 || freq["timestamp"] > 0
		return hasTS && hasKeysAtFraction(freq, total, 0.80, "level", "msg")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Zap JSON")
	},
}

// ---------- ECS JSON ----------

var ecsJSONProbe = structuredProbe{
	Name: "ECS JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return freq["@timestamp"]*100/total >= 80 && (freq["log.level"] > 0 || freq["ecs.version"] > 0)
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:ECS JSON")
	},
}

// ---------- CloudTrail JSON ----------

var cloudTrailJSONProbe = structuredProbe{
	Name: "CloudTrail JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return hasKeysAtFraction(freq, total, 0.80, "eventVersion", "eventTime", "eventSource", "eventName")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:CloudTrail JSON")
	},
}

// ---------- Suricata EVE JSON ----------

var suricataEVEProbe = structuredProbe{
	Name: "Suricata EVE",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return hasKeysAtFraction(freq, total, 0.80, "timestamp", "event_type", "src_ip")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Suricata EVE")
	},
}

// ---------- Kubernetes audit JSON ----------

var kubernetesAuditJSONProbe = structuredProbe{
	Name: "Kubernetes Audit JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return freq["kind"]*100/total >= 80 && hasKeysAtFraction(freq, total, 0.80, "auditID", "stage", "verb")
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Kubernetes Audit JSON")
	},
}

// ---------- auditd JSON ----------

var auditdJSONProbe = structuredProbe{
	Name: "auditd JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return hasKeysAtFraction(freq, total, 0.80, "type", "msg") &&
			(freq["audit_type"] > 0 || freq["auid"] > 0 || freq["ses"] > 0 || freq["uid"] > 0)
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:auditd JSON")
	},
}

// ---------- Vault audit JSON ----------

var vaultAuditJSONProbe = structuredProbe{
	Name: "Vault Audit JSON",
	Likely: func(sample []string) bool {
		total, freq := keyFreq(sample)
		if total == 0 {
			return false
		}
		return hasKeysAtFraction(freq, total, 0.80, "time", "type") &&
			(freq["auth"] > 0 || freq["request"] > 0 || freq["response"] > 0)
	},
	Render: func(sample []string) (string, string, bool) {
		return renderJSONSkeleton(sample, "structured:Vault Audit JSON")
	},
}

// ---------- logfmt ----------

var logfmtProbe = structuredProbe{
	Name:   "logfmt",
	Likely: looksLikeLogfmt,
	Render: func(sample []string) (string, string, bool) {
		return `%{GREEDYDATA:kvpairs}`, "structured:logfmt", true
	},
}

func looksLikeLogfmt(sample []string) bool {
	if len(sample) == 0 {
		return false
	}
	hits := 0
	for _, line := range sample {
		if logfmtFraction(line) >= 0.70 {
			hits++
		}
	}
	return float64(hits)/float64(len(sample)) >= 0.80
}

func logfmtFraction(line string) float64 {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return 0
	}
	kv := 0
	for _, t := range tokens {
		if i := strings.IndexByte(t, '='); i > 0 && i < len(t)-1 {
			head := t[:i]
			if isValidName(head) {
				kv++
			}
		}
	}
	return float64(kv) / float64(len(tokens))
}

// ---------- CEF ----------

var cefProbe = structuredProbe{
	Name: "CEF",
	Likely: func(sample []string) bool {
		hits := 0
		for _, line := range sample {
			if strings.Contains(line, "CEF:") && strings.Count(line, "|") >= 7 {
				hits++
			}
		}
		return len(sample) > 0 && float64(hits)/float64(len(sample)) >= 0.85
	},
	Render: func(sample []string) (string, string, bool) {
		return `(?:%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} )?CEF:%{INT:cef_version}\|%{DATA:vendor}\|%{DATA:product}\|%{DATA:product_version}\|%{DATA:signature}\|%{DATA:name}\|%{DATA:severity}\|%{GREEDYDATA:extensions}`,
			"structured:CEF", true
	},
}

// ---------- LEEF ----------

var leefProbe = structuredProbe{
	Name: "LEEF",
	Likely: func(sample []string) bool {
		hits := 0
		for _, line := range sample {
			if strings.Contains(line, "LEEF:") && strings.Count(line, "|") >= 4 {
				hits++
			}
		}
		return len(sample) > 0 && float64(hits)/float64(len(sample)) >= 0.85
	},
	Render: func(sample []string) (string, string, bool) {
		return `LEEF:%{NOTSPACE:leef_version}\|%{DATA:vendor}\|%{DATA:product}\|%{DATA:product_version}\|%{DATA:event_id}\|%{GREEDYDATA:extensions}`,
			"structured:LEEF", true
	},
}

// ---------- W3C / IIS ----------

var w3cIISProbe = structuredProbe{
	Name:   "W3C IIS",
	Likely: looksLikeW3C,
	Render: renderW3C,
}

func looksLikeW3C(sample []string) bool {
	for _, line := range sample {
		if strings.HasPrefix(line, "#Fields:") {
			return true
		}
	}
	return false
}

func renderW3C(sample []string) (string, string, bool) {
	fieldsLine := ""
	for _, line := range sample {
		if !strings.HasPrefix(line, "#Fields:") {
			continue
		}
		if fieldsLine != "" && fieldsLine != line {
			// Multiple distinct #Fields: headers in the same input
			// (e.g. concatenated rotations with different schemas).
			// Refuse to guess a schema; let other stages handle it.
			return "", "", false
		}
		fieldsLine = line
	}
	if fieldsLine == "" {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(fieldsLine, "#Fields:"))
	cols := strings.Fields(rest)
	if len(cols) == 0 {
		return "", "", false
	}
	parts := make([]string, 0, len(cols)*2)
	for i, col := range cols {
		if i > 0 {
			parts = append(parts, " ")
		}
		parts = append(parts, w3cColumnGrok(col))
	}
	return strings.Join(parts, ""), "structured:W3C IIS", true
}

func w3cColumnGrok(col string) string {
	name := canonicalName(strings.ReplaceAll(col, "-", "_"))
	if !isValidName(name) {
		name = "field"
	}
	switch col {
	case "date":
		return "%{DATE:" + name + "}"
	case "time":
		return "%{TIME:" + name + "}"
	case "c-ip", "s-ip":
		return "%{IP:" + name + "}"
	case "sc-status", "sc-substatus", "sc-win32-status", "sc-bytes", "cs-bytes", "time-taken":
		return "%{INT:" + name + "}"
	case "cs-method":
		return "%{WORD:" + name + "}"
	default:
		return "%{NOTSPACE:" + name + "}"
	}
}

// ---------- Loghub text formats ----------

var androidLogcatLineRe = regexp.MustCompile(`^\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\.\d{3}\s+\d+\s+\d+\s+[A-Z]\s+\S+:`)

var androidLogcatProbe = structuredProbe{
	Name:   "Android Logcat",
	Likely: func(sample []string) bool { return lineMatchFraction(sample, androidLogcatLineRe) >= 0.80 },
	Render: func(sample []string) (string, string, bool) {
		return `%{MONTHNUM2:month}-%{MONTHDAY2:day} %{TIME:time}\s+%{INT:pid}\s+%{INT:tid} %{WORD:level} %{NOTSPACE:tag}: %{GREEDYDATA:message}`,
			"structured:Android Logcat", true
	},
}

var healthAppLineRe = regexp.MustCompile(`^\d{8}-\d{1,2}:\d{1,2}:\d{1,2}:\d+\|[^|]+\|\d+\|`)

var healthAppProbe = structuredProbe{
	Name:   "HealthApp",
	Likely: func(sample []string) bool { return lineMatchFraction(sample, healthAppLineRe) >= 0.80 },
	Render: func(sample []string) (string, string, bool) {
		return `%{INT:date}-%{INT:hour}:%{INT:minute}:%{INT:second}:%{INT:millis}\|%{NOTSPACE:component}\|%{INT:pid}\|%{GREEDYDATA:message}`,
			"structured:HealthApp", true
	},
}

var sparkLineRe = regexp.MustCompile(`^\d{2}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s+[A-Z]+\s+\S+:`)

var sparkProbe = structuredProbe{
	Name:   "Spark",
	Likely: func(sample []string) bool { return lineMatchFraction(sample, sparkLineRe) >= 0.80 },
	Render: func(sample []string) (string, string, bool) {
		return `%{INT:year}/%{INT:month}/%{INT:day} %{TIME:time} %{LOGLEVEL:level} %{NOTSPACE:logger}: %{GREEDYDATA:message}`,
			"structured:Spark", true
	},
}

var windowsCBSLineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2},\s+\S+\s+CBS\s+`)

var windowsCBSProbe = structuredProbe{
	Name:   "Windows CBS",
	Likely: func(sample []string) bool { return lineMatchFraction(sample, windowsCBSLineRe) >= 0.80 },
	Render: func(sample []string) (string, string, bool) {
		return `%{TIMESTAMP_ISO8601:timestamp}, %{LOGLEVEL:level}\s+%{NOTSPACE:component}\s+%{GREEDYDATA:message}`,
			"structured:Windows CBS", true
	},
}

func lineMatchFraction(sample []string, re *regexp.Regexp) float64 {
	if len(sample) == 0 {
		return 0
	}
	hits := 0
	for _, line := range sample {
		if re.MatchString(line) {
			hits++
		}
	}
	return float64(hits) / float64(len(sample))
}

// ---------- CSV / TSV ----------

var csvProbe = structuredProbe{
	Name:   "CSV",
	Likely: func(sample []string) bool { return looksLikeDelim(sample, ',') },
	Render: func(sample []string) (string, string, bool) { return renderDelim(sample, ',', "structured:CSV") },
}

var tsvProbe = structuredProbe{
	Name:   "TSV",
	Likely: func(sample []string) bool { return looksLikeDelim(sample, '\t') },
	Render: func(sample []string) (string, string, bool) { return renderDelim(sample, '\t', "structured:TSV") },
}

func looksLikeDelim(sample []string, delim rune) bool {
	if len(sample) < 2 {
		return false
	}
	rdr := csv.NewReader(strings.NewReader(strings.Join(sample, "\n")))
	rdr.Comma = delim
	rdr.FieldsPerRecord = -1
	rdr.LazyQuotes = true
	counts := make(map[int]int)
	rows := 0
	for {
		rec, err := rdr.Read()
		if err != nil {
			break
		}
		rows++
		counts[len(rec)]++
		if rows >= 256 {
			break
		}
	}
	if rows == 0 {
		return false
	}
	bestCount, bestN := 0, 0
	for n, c := range counts {
		if c > bestCount {
			bestCount = c
			bestN = n
		}
	}
	if bestN < 3 {
		return false
	}
	return float64(bestCount)/float64(rows) >= 0.95
}

func renderDelim(sample []string, delim rune, source string) (string, string, bool) {
	rdr := csv.NewReader(strings.NewReader(strings.Join(sample, "\n")))
	rdr.Comma = delim
	rdr.FieldsPerRecord = -1
	rdr.LazyQuotes = true
	rows := make([][]string, 0, len(sample))
	for {
		rec, err := rdr.Read()
		if err != nil {
			break
		}
		rows = append(rows, rec)
		if len(rows) >= 256 {
			break
		}
	}
	if len(rows) == 0 {
		return "", "", false
	}
	counts := make(map[int]int)
	for _, r := range rows {
		counts[len(r)]++
	}
	var bestN, bestC int
	for n, c := range counts {
		if c > bestC {
			bestC = c
			bestN = n
		}
	}
	if bestN < 3 {
		return "", "", false
	}
	colExpr := `(?:"(?:""|[^"])*"|[^,]*)`
	sep := `,`
	if delim == '\t' {
		colExpr = `[^\t]*`
		sep = "\t"
	}
	parts := make([]string, 0, bestN*2)
	for i := 0; i < bestN; i++ {
		if i > 0 {
			parts = append(parts, sep)
		}
		parts = append(parts, colExpr)
	}
	return strings.Join(parts, ""), source, true
}

// stableKeys returns sorted keys of a map for deterministic iteration.
func stableKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ = stableKeys // reserved for diagnostics
