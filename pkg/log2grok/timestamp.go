package log2grok

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// TimestampHint describes how a pattern's matched fields can be assembled
// into a wall-clock time. It is derived by inspecting which Grok
// primitives the pattern uses (HTTPDATE, TIMESTAMP_ISO8601,
// SYSLOGTIMESTAMP, etc.) and which field name(s) capture them. Callers
// that need a time.Time can pass it (plus a LineResult.Fields) to
// ParseTimestamp.
//
// Field is the capture name that holds the timestamp string when the
// pattern emits a single combined field (e.g. HTTPDATE → "timestamp").
// Layout is the matching Go time.Parse layout. Both blank means we
// could not infer a hint from the pattern; callers should fall back to
// their own resolver.
//
// MultiField is set when the pattern splits the timestamp across
// several captures (e.g. SYSLOGTIMESTAMP exposes month/day/time but no
// year). In that case Field/Layout are blank and callers should
// assemble the time themselves from the listed sub-fields. The hint is
// purely advisory — no error if the input doesn't match the inferred
// shape.
type TimestampHint struct {
	Field      string   // capture name holding the timestamp
	Layout     string   // Go time.Parse layout
	MultiField []string // sub-fields when no single field carries the timestamp
	Source     string   // grok primitive the hint was derived from (for diagnostics)
}

// HasField reports whether the hint identifies a single timestamp
// capture (the most common case).
func (h TimestampHint) HasField() bool { return h.Field != "" && h.Layout != "" }

// IsZero reports whether the hint contains no usable information.
func (h TimestampHint) IsZero() bool {
	return h.Field == "" && h.Layout == "" && len(h.MultiField) == 0
}

// primitiveLayouts maps known Grok primitives to the Go time layout
// that ParseTimestamp can use to decode them. The list is intentionally
// limited to formats that are unambiguous on their own — others (year
// missing, month-as-name without year) need MultiField assembly.
var primitiveLayouts = []struct {
	Primitive string
	Layout    string
}{
	{"TIMESTAMP_ISO8601", "2006-01-02T15:04:05.000Z07:00"},
	{"ISO8601", "2006-01-02T15:04:05.000Z07:00"},
	{"DATESTAMP_RFC3339", time.RFC3339Nano},
	{"HTTPDATE", "02/Jan/2006:15:04:05 -0700"},
	{"HTTPDATE_CONDENSED", "02/Jan/2006 15:04:05"},
	{"DATESTAMP", "2006-01-02 15:04:05"},
	{"DATESTAMP_OTHER", "Mon Jan _2 15:04:05 MST 2006"},
	{"DATESTAMP_RFC822", "Mon Jan _2 2006 15:04:05 MST"},
}

// inferTimestampHint inspects a grok pattern body and returns the most
// specific TimestampHint it can derive. It looks at every `%{NAME:field}`
// reference; the *first* one whose primitive appears in primitiveLayouts
// wins. For SYSLOGTIMESTAMP and similar split formats we surface a
// MultiField hint instead.
//
// This is a static analysis — no input is parsed. Callers should always
// treat the hint as advisory and fall back to their own resolver when
// parsing fails.
func inferTimestampHint(grok string) TimestampHint {
	if grok == "" {
		return TimestampHint{}
	}
	refs := grokRefHintRe.FindAllStringSubmatch(grok, -1)
	if len(refs) == 0 {
		return TimestampHint{}
	}

	for _, ref := range refs {
		name, field := ref[1], ref[2]
		if field == "" {
			continue
		}
		for _, p := range primitiveLayouts {
			if name == p.Primitive {
				return TimestampHint{
					Field:  field,
					Layout: p.Layout,
					Source: name,
				}
			}
		}
	}

	// Look for SYSLOGTIMESTAMP / SYSLOGTIMESTAMP_RFC3164-style splits.
	// These primitives don't expose a single composite field; instead
	// callers typically capture month/day/time separately.
	for _, ref := range refs {
		if ref[1] == "SYSLOGTIMESTAMP" || ref[1] == "SYSLOGTIMESTAMP_RFC3164" {
			return TimestampHint{
				Source:     ref[1],
				MultiField: []string{"month", "day", "time"},
			}
		}
	}

	// Last resort: structured probe outputs often capture a single
	// field literally named "timestamp" with an ISO8601 layout — surface
	// that without a primitive name.
	for _, ref := range refs {
		if ref[2] == "timestamp" || ref[2] == "@timestamp" {
			return TimestampHint{
				Field:  ref[2],
				Layout: "2006-01-02T15:04:05.000Z07:00",
				Source: ref[1],
			}
		}
	}

	return TimestampHint{}
}

// grokRefHintRe is a copy of internal/pattern.grokRefRe (we don't import
// internals from here). Keep them in sync — both match
// %{NAME}, %{NAME:field}, %{NAME:field:type}.
var grokRefHintRe = regexp.MustCompile(`%\{(\w+)(?::([\w.@-]+)(?::(\w+))?)?\}`)

// ErrNoTimestamp is returned by ParseTimestamp when the supplied hint
// has no usable field or layout, or when the named field is missing
// from the supplied capture map.
var ErrNoTimestamp = errors.New("log2grok: no timestamp captured")

// ParseTimestamp turns a captured field into a time.Time using the
// hint's layout. Layouts that omit a timezone parse in UTC. The helper
// understands a small grammar of common variants — trailing "Z",
// fractional seconds with `,` or `.`, single-digit days padded with
// space — so simple cases don't require callers to wire up a separate
// timeresolve layer.
//
// Returns ErrNoTimestamp if the hint is empty or the field is missing.
func ParseTimestamp(fields map[string]string, hint TimestampHint) (time.Time, error) {
	if !hint.HasField() {
		return time.Time{}, ErrNoTimestamp
	}
	raw, ok := fields[hint.Field]
	if !ok || raw == "" {
		return time.Time{}, ErrNoTimestamp
	}
	return parseTimestampValue(raw, hint.Layout)
}

func parseTimestampValue(raw, layout string) (time.Time, error) {
	candidates := []string{layout}

	// Common normalisations: comma fractional separator (some Java/
	// log4j layouts), trailing Z without colon in the layout, and a
	// shorter "yyyy-MM-dd HH:mm:ss" fallback when the input lacks
	// fractional seconds.
	norm := strings.Replace(raw, ",", ".", 1)
	if t, err := time.Parse(layout, norm); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, norm); err == nil {
		return t, nil
	}

	for _, alt := range []string{
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
	} {
		candidates = append(candidates, alt)
		if t, err := time.Parse(alt, raw); err == nil {
			return t, nil
		}
		if t, err := time.Parse(alt, norm); err == nil {
			return t, nil
		}
	}

	return time.Time{}, ErrNoTimestamp
}

// inferTimestampHintFromFields is a last-ditch heuristic used by Discover
// for patterns where the grok body did not contain a recognised
// primitive (e.g. drain-generated patterns that surfaced a `timestamp`
// capture but used %{DATA} for it). It scans the field names of the
// supplied sample and returns a hint when one of the canonical names
// (timestamp / @timestamp / time) is present.
func inferTimestampHintFromFields(fields map[string]string) TimestampHint {
	for _, name := range []string{"timestamp", "@timestamp", "time"} {
		if v, ok := fields[name]; ok && v != "" {
			return TimestampHint{
				Field:  name,
				Layout: "2006-01-02T15:04:05.000Z07:00",
				Source: "field-name",
			}
		}
	}
	return TimestampHint{}
}
