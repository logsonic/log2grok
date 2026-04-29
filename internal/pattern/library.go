package pattern

import (
	"regexp"
	"sort"
	"strings"
)

// KnownPattern is one library entry.
type KnownPattern struct {
	Name           string
	Pattern        string
	Priority       int
	Specificity    int
	Description    string
	CustomPatterns map[string]string
}

// KnownPatterns is the merged, deduplicated, sorted library.
// Populated in init() in bundle.go after bundled packs are ingested.
var KnownPatterns []KnownPattern

func sortKnownPatterns(in []KnownPattern) {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Priority != in[j].Priority {
			return in[i].Priority < in[j].Priority
		}
		if in[i].Specificity != in[j].Specificity {
			return in[i].Specificity > in[j].Specificity
		}
		return in[i].Name < in[j].Name
	})
}

func dedupKnownPatterns(in []KnownPattern) []KnownPattern {
	seen := make(map[string]struct{}, len(in))
	out := make([]KnownPattern, 0, len(in))
	for _, kp := range in {
		key := normalizeForDedup(kp.Pattern) + "|" + sortedFieldNames(kp.Pattern)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, kp)
	}
	return out
}

var dedupSpaceRe = regexp.MustCompile(`\s+`)
var dedupFieldRe = regexp.MustCompile(`%\{(\w+)(?::(\w+)(?::\w+)?)?\}`)

func normalizeForDedup(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "^")
	s = strings.TrimSuffix(s, "$")
	s = strings.ReplaceAll(s, "(?<", "(?P<")
	s = dedupSpaceRe.ReplaceAllString(s, " ")
	return s
}

func sortedFieldNames(p string) string {
	matches := dedupFieldRe.FindAllStringSubmatch(p, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if m[2] != "" {
			names = append(names, m[2])
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
