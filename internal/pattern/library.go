package pattern

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// KnownPattern is one library entry.
type KnownPattern struct {
	Name           string            `json:"name"`
	Pattern        string            `json:"pattern"`
	Priority       int               `json:"priority"`
	Specificity    int               `json:"specificity"`
	Description    string            `json:"description,omitempty"`
	CustomPatterns map[string]string `json:"customPatterns,omitempty"`
}

// DefaultPatternDescription returns a short catalogue line for patterns
// whose JSON omitted "description" (the embedded library historically
// shipped name + grok body only).
func DefaultPatternDescription(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return fmt.Sprintf("Grok pattern for %s logs.", name)
}

// FillEmptyDescriptionsInPlace sets Description where it is blank, using
// DefaultPatternDescription(Name). Call after unmarshalling patterns
// from disk or embedded JSON so APIs and UIs always have copy to show.
func FillEmptyDescriptionsInPlace(lib []KnownPattern) {
	for i := range lib {
		if strings.TrimSpace(lib[i].Description) == "" && strings.TrimSpace(lib[i].Name) != "" {
			lib[i].Description = DefaultPatternDescription(lib[i].Name)
		}
	}
}

// KnownPatterns is the merged, deduplicated, sorted library.
// Populated by RefreshLibrary from KnownPatternsLibrary.
var KnownPatterns []KnownPattern

// composeKnownPatterns rebuilds KnownPatterns from KnownPatternsLibrary.
// Dedup and sort are applied so the matcher sees a deterministic,
// uniqued list.
func composeKnownPatterns() {
	KnownPatterns = make([]KnownPattern, 0, len(KnownPatternsLibrary))
	KnownPatterns = append(KnownPatterns, KnownPatternsLibrary...)
	KnownPatterns = dedupKnownPatterns(KnownPatterns)
	sortKnownPatterns(KnownPatterns)
}

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
		key := normalizeForDedup(kp.Pattern) + "|" +
			sortedFieldNames(kp.Pattern) + "|" +
			customPatternsKey(kp.CustomPatterns)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, kp)
	}
	return out
}

// customPatternsKey produces a stable serialization of a CustomPatterns
// map suitable for use in a dedup key. Two entries with the same Pattern
// text but different CustomPatterns must not collapse, since their
// effective regex bodies differ.
func customPatternsKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte('\x00')
	}
	return b.String()
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
