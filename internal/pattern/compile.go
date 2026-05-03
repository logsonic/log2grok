package pattern

import (
	"errors"
	"fmt"
	"regexp"
)

// grokRefRe matches %{NAME}, %{NAME:field}, and %{NAME:field:type}.
// The optional :type tail (Logstash type-cast) is parsed for compatibility
// and ignored at compile time.
var grokRefRe = regexp.MustCompile(`%\{(\w+)(?::([\w.@-]+)(?::(\w+))?)?\}`)

// goNamedGroupRe matches Go's (?P<name>...) named-group opener inside an
// already-expanded body.
var goNamedGroupRe = regexp.MustCompile(`\(\?P<(\w+)>`)

// logstashNamedGroupRe matches Logstash's (?<name>...) named-group opener.
var logstashNamedGroupRe = regexp.MustCompile(`\(\?<(\w+)>`)

// CompileGrok expands all %{NAME}, %{NAME:field}, and %{NAME:field:type}
// references using GrokPrimitives plus any extras, then compiles the result
// as an anchored Go regexp. Anchoring is automatic.
func CompileGrok(pattern string, extras map[string]string) (*regexp.Regexp, error) {
	used := make(map[string]int)
	expanded, err := expandGrok(pattern, extras, used, 0)
	if err != nil {
		return nil, err
	}
	return regexp.Compile(`^` + expanded + `\r?$`)
}

func expandGrok(s string, extras map[string]string, used map[string]int, depth int) (string, error) {
	if depth > 32 {
		return "", errors.New("grok expansion too deep (cycle?)")
	}

	s = logstashNamedGroupRe.ReplaceAllString(s, `(?P<$1>`)

	var firstErr error
	out := grokRefRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := grokRefRe.FindStringSubmatch(match)
		name, field := sub[1], sub[2]
		_ = sub[3]

		var (
			body string
			ok   bool
		)
		if extras != nil {
			body, ok = extras[name]
		}
		if !ok {
			body, ok = GrokPrimitives[name]
		}
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown grok primitive %%{%s}", name)
			}
			return match
		}

		expanded, err := expandGrok(body, extras, used, depth+1)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}

		expanded = dedupNamedGroups(expanded, used)

		field = sanitizeFieldName(field)
		if field != "" {
			unique := uniqueName(field, used)
			return "(?P<" + unique + ">" + expanded + ")"
		}
		return "(?:" + expanded + ")"
	})

	return out, firstErr
}

func sanitizeFieldName(s string) string {
	if s == "" {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return ""
	}
	first := out[0]
	if first >= '0' && first <= '9' {
		out = append([]byte{'_'}, out...)
	}
	return string(out)
}

func uniqueName(field string, used map[string]int) string {
	if used[field] == 0 {
		used[field] = 1
		return field
	}
	used[field]++
	return fmt.Sprintf("%s_%d", field, used[field])
}

func dedupNamedGroups(body string, used map[string]int) string {
	return goNamedGroupRe.ReplaceAllStringFunc(body, func(open string) string {
		m := goNamedGroupRe.FindStringSubmatch(open)
		unique := uniqueName(m[1], used)
		return "(?P<" + unique + ">"
	})
}
