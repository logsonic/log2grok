package pattern

import (
	"regexp"
	"strings"
)

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
	"latency":    "duration", "rt": "duration",
	"trace": "trace_id", "traceid": "trace_id",
	"span": "span_id", "spanid": "span_id",
	"id": "id", "ip": "ip", "ua": "user_agent",
}

func canonicalName(s string) string {
	s = strings.Trim(s, `"'[](){}<>`)
	s = strings.ToLower(strings.ReplaceAll(s, "-", "_"))
	if mapped, ok := canonicalNames[s]; ok {
		return mapped
	}
	return s
}
