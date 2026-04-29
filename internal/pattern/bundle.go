package pattern

import (
	"fmt"
	"sort"
	"strings"
)

// BundledLoadDiagnostics captures pack-loading issues (entries dropped for
// being unsupported under RE2, parse failures, etc.). Exposed for tests.
var BundledLoadDiagnostics []error

// topLevelByPack lists names whose entries we promote into KnownPatternsBundled.
// Anything else is treated as a primitive only.
// Bundled top-level entries are deliberately given low Specificity so that
// curated entries (which are written to match the expected golden output
// exactly) win on tie-broken candidates with the same coverage.
var topLevelByPack = map[string][]bundledTopLevel{
	"logstash_ecs_v1": {
		{Name: "HTTPD Common Log", PrimitiveName: "HTTPD_COMMONLOG", Priority: 500, Specificity: 30},
		{Name: "HTTPD Combined Log", PrimitiveName: "HTTPD_COMBINEDLOG", Priority: 500, Specificity: 30},
		{Name: "Syslog Base", PrimitiveName: "SYSLOGBASE", Priority: 520, Specificity: 25},
	},
	"logstash_legacy": {
		{Name: "Syslog 5424 Line", PrimitiveName: "SYSLOG5424LINE", Priority: 510, Specificity: 30},
	},
	"vjeantet_core": {
		{Name: "Nagios Log Line", PrimitiveName: "NAGIOSLOGLINE", Priority: 540, Specificity: 30},
		{Name: "Rails 3", PrimitiveName: "RAILS3", Priority: 541, Specificity: 25},
		{Name: "Redis Log Bracketed", PrimitiveName: "REDISLOG", Priority: 542, Specificity: 25},
		{Name: "MongoDB 3 Log", PrimitiveName: "MONGO3_LOG", Priority: 543, Specificity: 30},
	},
}

type bundledTopLevel struct {
	Name          string
	PrimitiveName string
	Priority      int
	Specificity   int
}

func init() {
	loadBundledPacks()
	composeKnownPatterns()
}

func loadBundledPacks() {
	for _, pack := range BuiltinPatternPacks {
		entries := parsePackText(pack.PatternText)
		// Add primitives that don't already exist in the override map.
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			body := convertPCREToRE2(entries[name])
			if body == "" {
				continue
			}
			if _, ok := GrokPrimitives[name]; ok {
				continue
			}
			if _, ok := GrokPrimitivesBundled[name]; ok {
				continue
			}
			GrokPrimitivesBundled[name] = body
		}

		// Promote selected top-level entries into KnownPatternsBundled.
		for _, top := range topLevelByPack[pack.Name] {
			body, ok := entries[top.PrimitiveName]
			if !ok {
				BundledLoadDiagnostics = append(BundledLoadDiagnostics,
					fmt.Errorf("pack %s: missing top-level %q", pack.Name, top.PrimitiveName))
				continue
			}
			body = convertPCREToRE2(body)
			if body == "" {
				continue
			}
			KnownPatternsBundled = append(KnownPatternsBundled, KnownPattern{
				Name:        top.Name,
				Pattern:     body,
				Priority:    top.Priority,
				Specificity: top.Specificity,
				Description: fmt.Sprintf("Bundled from %s (%s)", pack.Name, top.PrimitiveName),
			})
		}
	}
	sort.SliceStable(KnownPatternsBundled, func(i, j int) bool {
		return KnownPatternsBundled[i].Name < KnownPatternsBundled[j].Name
	})
}

func composeKnownPatterns() {
	KnownPatterns = make([]KnownPattern, 0,
		len(KnownPatternsGolden)+len(KnownPatternsBundled)+len(KnownPatternsCurated)+len(KnownPatternsCatchall))
	KnownPatterns = append(KnownPatterns, KnownPatternsGolden...)
	KnownPatterns = append(KnownPatterns, KnownPatternsBundled...)
	KnownPatterns = append(KnownPatterns, KnownPatternsCurated...)
	KnownPatterns = append(KnownPatterns, KnownPatternsCatchall...)
	KnownPatterns = dedupKnownPatterns(KnownPatterns)
	sortKnownPatterns(KnownPatterns)
}

// parsePackText parses NAME REGEX lines from a pack snapshot. Comment lines
// (# ...) and blank lines are ignored. A line continuation is not supported.
func parsePackText(text string) map[string]string {
	out := make(map[string]string)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r ")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.IndexAny(line, " \t")
		if sp <= 0 {
			continue
		}
		name := line[:sp]
		body := strings.TrimSpace(line[sp+1:])
		if name == "" || body == "" {
			continue
		}
		out[name] = body
	}
	return out
}

// convertPCREToRE2 applies a sequence of safe rewrites that move PCRE-style
// constructs into RE2-compatible forms. Returns the empty string when the
// pattern can't be safely converted (caller drops it).
func convertPCREToRE2(s string) string {
	// 1. Logstash named groups → Go form.
	s = logstashNamedGroupRe.ReplaceAllString(s, `(?P<$1>`)
	// 2. Atomic groups (?>...) → non-capturing.
	s = strings.ReplaceAll(s, "(?>", "(?:")
	// 3. Strip lookaround groups: (?=...), (?!...), (?<=...), (?<!...).
	s = stripLookaround(s)
	// 4. Possessive quantifiers: *+, ++, ?+, }+, +- after }.
	s = stripPossessive(s)
	return s
}

func stripLookaround(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		// Detect (?= (?! (?<= (?<!
		if i+3 < len(s) && s[i] == '(' && s[i+1] == '?' {
			head := s[i+2]
			isLookahead := head == '=' || head == '!'
			isLookbehind := false
			if head == '<' && i+4 < len(s) && (s[i+3] == '=' || s[i+3] == '!') {
				isLookbehind = true
			}
			if isLookahead || isLookbehind {
				// Find matching ).
				end := matchParen(s, i)
				if end > 0 {
					i = end + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// matchParen returns the index of the closing ) for an opening ( at idx,
// honoring backslash escapes, nested parens, and character classes.
func matchParen(s string, idx int) int {
	if idx >= len(s) || s[idx] != '(' {
		return -1
	}
	depth := 0
	inClass := false
	for i := idx; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			}
			continue
		}
		switch c {
		case '[':
			inClass = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func stripPossessive(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c == '*' || c == '+' || c == '?' || c == '}') && i+1 < len(s) && s[i+1] == '+' {
			// Check this isn't already consumed by the previous loop step.
			if c == '+' && i > 0 && s[i-1] == '+' {
				// Already a "++" tail of a previous match — write as-is.
				b.WriteByte(c)
				continue
			}
			b.WriteByte(c)
			// Skip the trailing +.
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
