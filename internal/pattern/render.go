package pattern

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Render walks the sample line one rune at a time and emits a Grok pattern.
// At each position: if a field starts here, emit %{TYPE:name} and skip to
// field end. Else if an uncovered slot starts here, emit
// %{NOTSPACE:unparsed_N}. Else emit one regex-escaped rune and advance.
func Render(sample string, fields []field, slots []slotRange) string {
	sorted := append([]field(nil), fields...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	covered := make(map[int]bool)
	for _, s := range slots {
		for _, f := range sorted {
			if f.Start == s.Start && f.End == s.End {
				covered[s.SlotIndex] = true
				break
			}
		}
	}

	slotByStart := make(map[int]slotRange, len(slots))
	for _, s := range slots {
		slotByStart[s.Start] = s
	}

	var b strings.Builder
	cursor, fieldI, unparsedN := 0, 0, 0

	for cursor < len(sample) {
		if fieldI < len(sorted) && sorted[fieldI].Start == cursor {
			f := sorted[fieldI]
			b.WriteString("%{")
			b.WriteString(f.GrokType)
			b.WriteString(":")
			b.WriteString(f.Name)
			b.WriteString("}")
			cursor = f.End
			fieldI++
			continue
		}
		if s, ok := slotByStart[cursor]; ok && !covered[s.SlotIndex] && s.End > s.Start {
			unparsedN++
			b.WriteString("%{NOTSPACE:unparsed_")
			b.WriteString(strconv.Itoa(unparsedN))
			b.WriteString("}")
			cursor = s.End
			continue
		}
		r, size := utf8.DecodeRuneInString(sample[cursor:])
		if r == utf8.RuneError && size <= 1 {
			b.WriteString(regexp.QuoteMeta(sample[cursor : cursor+1]))
			cursor++
			continue
		}
		b.WriteString(regexp.QuoteMeta(sample[cursor : cursor+size]))
		cursor += size
	}
	return b.String()
}
