package pattern

import (
	"errors"
	"sort"
)

// drainBackend is the abstraction the rest of the package depends on.
// The default implementation, defaultDrainBackend, calls into axiomhq/drain3.
type drainBackend interface {
	Train(lines []string) error
	Templates() []drainTemplate
	ClusterIDOf(line string) (int, bool)
	Tokenize(line string) []string
}

// drainTemplate is the post-clustering shape we consume.
type drainTemplate struct {
	ID        int
	Tokens    []string // each entry is either a literal token or "<*>" for a slot
	LineCount int
}

// defaultBackend is used only for its stateless Tokenize helper (it reads
// the immutable config and never touches the matcher). Training must NOT
// use it: a drain3 Matcher is per-training mutable state, and Discover may
// leave a drain goroutine running in the background after a higher-priority
// stage auto-accepts, so two trainings can overlap. Sharing one backend
// across them would race on the matcher field. trainDrain builds a fresh
// backend per call instead.
var defaultBackend drainBackend = newAxiomDrain3Backend()

type cluster struct {
	ID            int
	Template      []tplPart
	TokenCount    int
	LineCount     int
	SampleLineIdx int
}

type tplPart struct {
	IsSlot bool
	Token  string
}

func trainDrain(lines []string) ([]cluster, error) {
	// Fresh backend per call: each training owns its mutable matcher, so
	// concurrent/overlapping Discover invocations never race. See the note
	// on defaultBackend.
	return trainDrainWith(newAxiomDrain3Backend(), lines)
}

func trainDrainWith(b drainBackend, lines []string) ([]cluster, error) {
	if b == nil {
		return nil, errors.New("nil drain backend")
	}
	if err := b.Train(lines); err != nil {
		return nil, err
	}
	tmpls := b.Templates()
	sampleIdx := indexFirstSampleLine(lines, b)

	out := make([]cluster, 0, len(tmpls))
	for _, t := range tmpls {
		idx, ok := sampleIdx[t.ID]
		if !ok {
			idx = -1
		}
		out = append(out, cluster{
			ID:            t.ID,
			Template:      buildTplParts(t),
			TokenCount:    len(t.Tokens),
			LineCount:     t.LineCount,
			SampleLineIdx: idx,
		})
	}
	// Sort clusters by LineCount desc so callers can rely on out[0] being
	// the dominant cluster regardless of upstream drain3 ordering.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LineCount != out[j].LineCount {
			return out[i].LineCount > out[j].LineCount
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// drainExtraDelimiters splits common key/value separators that would otherwise
// become opaque tokens.
var drainExtraDelimiters = []string{"=", ",", "|"}

func indexFirstSampleLine(lines []string, b drainBackend) map[int]int {
	out := make(map[int]int, 32)
	for i, line := range lines {
		if id, ok := b.ClusterIDOf(line); ok {
			if _, seen := out[id]; !seen {
				out[id] = i
			}
		}
	}
	return out
}

func buildTplParts(t drainTemplate) []tplPart {
	parts := make([]tplPart, 0, len(t.Tokens))
	for _, tok := range t.Tokens {
		if tok == "<*>" {
			parts = append(parts, tplPart{IsSlot: true})
		} else {
			parts = append(parts, tplPart{IsSlot: false, Token: tok})
		}
	}
	return parts
}
