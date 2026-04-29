package pattern

import "strings"

type tokenSpan struct {
	Start, End int
	Text       string
}

// tokenSpansOf locates each tokens[i] inside line in order. Two equal tokens
// in a line still get distinct spans because the search advances past each
// match before the next lookup.
func tokenSpansOf(line string, tokens []string) []tokenSpan {
	out := make([]tokenSpan, 0, len(tokens))
	cursor := 0
	for _, tok := range tokens {
		if tok == "<*>" || tok == "" {
			out = append(out, tokenSpan{Start: cursor, End: cursor})
			continue
		}
		idx := strings.Index(line[cursor:], tok)
		if idx < 0 {
			out = append(out, tokenSpan{Start: cursor, End: cursor})
			continue
		}
		start := cursor + idx
		end := start + len(tok)
		out = append(out, tokenSpan{Start: start, End: end, Text: tok})
		cursor = end
	}
	return out
}
