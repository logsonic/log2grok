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
	smartEmailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	smartURLRe   = regexp.MustCompile(`https?://[^\s]+`)
	smartMACRe   = regexp.MustCompile(`(?:[0-9A-Fa-f]{2}[:\-]){5}[0-9A-Fa-f]{2}`)
	smartUUIDRe  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
)

// smartCategories enumerates the keys produced by smartDecode in a stable
// order. Each entry pairs the output key with the regex that mines it.
// Output keys carry the leading underscore so callers can treat them as
// auxiliary metadata fields without further renaming.
var smartCategories = []struct {
	key string
	re  *regexp.Regexp
}{
	{"_ipv4_addr", smartIPv4Re},
	{"_email_addr", smartEmailRe},
	{"_urls", smartURLRe},
	{"_mac_addr", smartMACRe},
	{"_uuids", smartUUIDRe},
}

// smartDecode mines a single line for well-known entities. It returns a
// map keyed by category (e.g. "_ipv4_addr") whose values are
// comma-separated lists of the matches found, in the order they appear
// in the input. Categories with zero matches are omitted entirely so
// callers can range over the result without checking lengths. The
// caller owns the returned map and may freely mutate or discard it.
func smartDecode(line string) map[string]string {
	if line == "" {
		return nil
	}
	var out map[string]string
	for _, c := range smartCategories {
		hits := c.re.FindAllString(line, -1)
		if len(hits) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(smartCategories))
		}
		out[c.key] = strings.Join(hits, ", ")
	}
	return out
}
