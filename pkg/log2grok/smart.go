package log2grok

import (
	"regexp"
	"strings"
)

// Smart-decode regular expressions. They are intentionally lenient: the
// goal is to surface obvious entities (IP, email, URL, MAC, UUID) for
// downstream tooling, not to validate strict RFC syntax.
var (
	smartIPv4Re  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// IPv6 in either full ("8 groups of 1-4 hex") or compressed ("::") form.
	// Conservative left-anchor: previous char must not be a hex digit or
	// colon, so we don't gobble the tail of a longer identifier.
	smartIPv6Re = regexp.MustCompile(`(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,7}:[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{1,4}){0,6}`)
	smartEmailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	smartURLRe   = regexp.MustCompile(`https?://[^\s)>\]"']+`)
	smartMACRe   = regexp.MustCompile(`(?:[0-9A-Fa-f]{2}[:\-]){5}[0-9A-Fa-f]{2}`)
	smartUUIDRe  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
)

// SmartEntities is the typed counterpart to LineResult.Smart. The map
// form remains the wire-compatible default for callers that consume
// auxiliary entities by key (it preserves the historical `_ipv4_addr,
// _email_addr, _urls, _mac_addr, _uuids` keys); the struct form lets
// callers consume them as proper slices without re-splitting the
// comma-joined map values. Both views are always populated together.
//
// Slices preserve discovery order and may contain duplicates if the
// same entity appears multiple times in a single line — callers are
// responsible for deduping if they care.
type SmartEntities struct {
	IPv4   []string
	IPv6   []string
	Emails []string
	URLs   []string
	MACs   []string
	UUIDs  []string
}

// Any reports whether the struct holds at least one entity. Useful for
// short-circuit checks without scanning every slice individually.
func (s SmartEntities) Any() bool {
	return len(s.IPv4) > 0 || len(s.IPv6) > 0 || len(s.Emails) > 0 ||
		len(s.URLs) > 0 || len(s.MACs) > 0 || len(s.UUIDs) > 0
}

// smartCategories enumerates the keys produced by smartDecode in a stable
// order. Each entry pairs the output key with the regex that mines it
// and the struct slice it populates. Output keys carry the leading
// underscore so callers can treat them as auxiliary metadata fields
// without further renaming.
var smartCategories = []struct {
	key    string
	re     *regexp.Regexp
	post   func([]string) []string // optional filter after regex
	assign func(*SmartEntities, []string)
}{
	{"_ipv4_addr", smartIPv4Re, nil, func(s *SmartEntities, v []string) { s.IPv4 = v }},
	{"_ipv6_addr", smartIPv6Re, filterIPv6Hits, func(s *SmartEntities, v []string) { s.IPv6 = v }},
	{"_email_addr", smartEmailRe, nil, func(s *SmartEntities, v []string) { s.Emails = v }},
	{"_urls", smartURLRe, nil, func(s *SmartEntities, v []string) { s.URLs = v }},
	{"_mac_addr", smartMACRe, nil, func(s *SmartEntities, v []string) { s.MACs = v }},
	{"_uuids", smartUUIDRe, nil, func(s *SmartEntities, v []string) { s.UUIDs = v }},
}

// macLikeRe matches the exact MAC shape (six 2-hex-char groups separated
// by `:` or `-`). The IPv6 regex is intentionally lenient and would also
// match MACs in colon notation; we strip those here.
var macLikeRe = regexp.MustCompile(`^(?:[0-9A-Fa-f]{2}[:\-]){5}[0-9A-Fa-f]{2}$`)

// filterIPv6Hits drops false positives from the lenient IPv6 regex.
// Real IPv6 either contains `::` (compressed form) or has at least one
// group longer than 2 hex chars or shorter than 2; bare MAC-shaped
// strings (six 2-hex groups) are rejected.
func filterIPv6Hits(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:0]
	for _, s := range in {
		if macLikeRe.MatchString(s) {
			continue
		}
		if strings.Contains(s, "::") {
			out = append(out, s)
			continue
		}
		// Real (non-compressed) IPv6 has eight groups; a colon-separated
		// hex string with fewer than eight groups but no `::` is almost
		// certainly something else (MAC variants, identifiers).
		if strings.Count(s, ":") >= 7 {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// smartDecode mines a single line for well-known entities. It returns
// (a) the legacy map form keyed by category with comma-separated values,
// and (b) the typed struct form. Categories with zero matches are
// omitted from the map (it stays nil if nothing was found) and leave
// their slices nil on the struct.
//
// IPv6 detection is conservative: it requires at least two ``:''
// separators so we don't false-positive on bare hex words. The legacy
// map only contains keys that produced hits, preserving the original
// wire shape; the new IPv6 key was added without breaking that — it
// appears only when an IPv6 address was actually seen.
func smartDecode(line string) (map[string]string, SmartEntities) {
	var entities SmartEntities
	if line == "" {
		return nil, entities
	}
	var out map[string]string
	for _, c := range smartCategories {
		hits := c.re.FindAllString(line, -1)
		if c.post != nil {
			hits = c.post(hits)
		}
		if len(hits) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(smartCategories))
		}
		out[c.key] = strings.Join(hits, ", ")
		c.assign(&entities, hits)
	}
	return out, entities
}
