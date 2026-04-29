package pattern

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// DiscoveredPattern is what Discover returns. ONE per call.
type DiscoveredPattern struct {
	Source         string
	SourceFamily   string
	Grok           string
	Coverage       float64
	MatchedCount   int
	TotalLines     int
	SampleLine     string
	Truncated      bool
	CustomPatterns map[string]string
}

// Options controls Discover's behavior.
type Options struct {
	LibraryThreshold float64
	MaxLines         int
	Verbose          bool
	Diagnostics      io.Writer
}

// ErrEmptyInput is returned when the input has no non-empty lines.
var ErrEmptyInput = errors.New("log2grok: no non-empty input lines")

// Discover returns the single best Grok pattern for the input lines.
func Discover(lines []string, opts Options) (*DiscoveredPattern, error) {
	considered, truncated := limitLines(lines, opts.MaxLines)
	normalized := normalizeLines(considered)
	if len(normalized.MatchLines) == 0 {
		return nil, ErrEmptyInput
	}

	threshold := opts.LibraryThreshold
	if threshold <= 0 {
		threshold = 0.85
	}
	diag := opts.Diagnostics
	if diag == nil {
		diag = io.Discard
	}

	sample := chooseSample(normalized.MatchLines, 4096)

	var best *DiscoveredPattern

	if dp := tryStructured(sample, normalized.MatchLines, diag); dp != nil {
		if dp.Coverage >= 0.90 {
			fmt.Fprintf(diag, "stage1 structured auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
			return markTruncated(dp, truncated), nil
		}
		best = pickBetter(best, dp)
	}

	if dp := tryLibrary(sample, normalized.MatchLines, threshold, diag); dp != nil {
		if dp.Coverage >= threshold {
			fmt.Fprintf(diag, "stage2 library auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
			return markTruncated(dp, truncated), nil
		}
		best = pickBetter(best, dp)
	}

	if dp, err := deriveFromDrain(normalized.MatchLines, diag); err == nil && dp != nil && dp.Grok != "" {
		if dp.Coverage >= 0.85 {
			fmt.Fprintf(diag, "stage3 drain auto-accept: coverage=%.3f\n", dp.Coverage)
			return markTruncated(dp, truncated), nil
		}
		best = pickBetter(best, dp)
	} else if err != nil {
		fmt.Fprintf(diag, "stage3 drain error: %v\n", err)
	}

	if best != nil {
		if best.MatchedCount == 0 {
			return markTruncated(deriveSafeFallback(normalized.MatchLines), truncated), nil
		}
		return markTruncated(best, truncated), nil
	}

	return markTruncated(deriveSafeFallback(normalized.MatchLines), truncated), nil
}

func limitLines(lines []string, max int) ([]string, bool) {
	if max > 0 && len(lines) > max {
		return lines[:max], true
	}
	return lines, false
}

func markTruncated(dp *DiscoveredPattern, truncated bool) *DiscoveredPattern {
	if dp != nil {
		dp.Truncated = truncated
	}
	return dp
}

// pickBetter compares two candidates by integer matched count, then typed
// captures, then source-family priority.
func pickBetter(a, b *DiscoveredPattern) *DiscoveredPattern {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.MatchedCount != a.MatchedCount:
		if b.MatchedCount > a.MatchedCount {
			return b
		}
		return a
	default:
		bt := typedCaptureCount(b.Grok)
		at := typedCaptureCount(a.Grok)
		if bt != at {
			if bt > at {
				return b
			}
			return a
		}
		if familyRank(b.SourceFamily) < familyRank(a.SourceFamily) {
			return b
		}
		return a
	}
}

func familyRank(family string) int {
	switch family {
	case "library":
		return 0
	case "structured":
		return 1
	case "drain":
		return 2
	case "fallback":
		return 3
	default:
		return 4
	}
}

func typedCaptureCount(grok string) int {
	n := 0
	for _, m := range grokRefRe.FindAllStringSubmatch(grok, -1) {
		name := m[1]
		fld := m[2]
		if fld == "" || strings.HasPrefix(fld, "unparsed_") || name == "GREEDYDATA" {
			continue
		}
		n++
	}
	return n
}

type normalizedInput struct {
	MatchLines   []string
	OriginalSize int
	BlankCount   int
}

func normalizeLines(lines []string) normalizedInput {
	out := normalizedInput{OriginalSize: len(lines)}
	for i, line := range lines {
		if i == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if line == "" {
			out.BlankCount++
			continue
		}
		out.MatchLines = append(out.MatchLines, line)
	}
	return out
}

func tryStructured(sample, all []string, diag io.Writer) *DiscoveredPattern {
	var best *DiscoveredPattern
	for _, probe := range structuredProbes {
		if !probe.Likely(sample) {
			continue
		}
		grok, source, ok := probe.Render(sample)
		if !ok {
			continue
		}
		re, err := CompileGrok(grok, nil)
		if err != nil {
			fmt.Fprintf(diag, "structured probe %s: compile failed: %v\n", probe.Name, err)
			continue
		}
		matched := EvaluateCoverage(re, all)
		dp := &DiscoveredPattern{
			Source:       source,
			SourceFamily: "structured",
			Grok:         grok,
			Coverage:     ratio(matched, len(all)),
			MatchedCount: matched,
			TotalLines:   len(all),
		}
		fmt.Fprintf(diag, "structured probe %s: matched=%d/%d\n", probe.Name, matched, len(all))
		best = pickBetter(best, dp)
	}
	return best
}

func tryLibrary(sample, all []string, threshold float64, diag io.Writer) *DiscoveredPattern {
	candidates := scoreLibraryOnSample(sample)
	candidates = keepTopCandidates(candidates, 12)

	var best *candidateResult
	for _, c := range candidates {
		if c.SampleCoverage < 0.50 {
			continue
		}
		c := c
		floor := -1
		if best != nil {
			floor = best.Matched
		}
		matched := evaluateCoverageWithFloor(c.Compiled, all, floor)
		result := &candidateResult{
			Pattern:   c.Pattern,
			Compiled:  c.Compiled,
			Matched:   matched,
			FullTotal: len(all),
		}
		fmt.Fprintf(diag, "library %s: sample=%.3f full=%d/%d\n",
			c.Pattern.Name, c.SampleCoverage, matched, len(all))
		if betterCandidate(result, best) {
			best = result
		}
	}
	if best == nil {
		return nil
	}
	return &DiscoveredPattern{
		Source:         "library:" + best.Pattern.Name,
		SourceFamily:   "library",
		Grok:           best.Pattern.Pattern,
		Coverage:       ratio(best.Matched, best.FullTotal),
		MatchedCount:   best.Matched,
		TotalLines:     best.FullTotal,
		CustomPatterns: best.Pattern.CustomPatterns,
	}
}

func deriveFromDrain(lines []string, diag io.Writer) (*DiscoveredPattern, error) {
	clusters, err := trainDrain(lines)
	if err != nil {
		return nil, err
	}
	if len(clusters) == 0 {
		return nil, errors.New("drain produced no clusters")
	}
	dominant := clusters[0]
	if dominant.SampleLineIdx < 0 || dominant.SampleLineIdx >= len(lines) {
		return nil, errors.New("drain dominant cluster has no representative line")
	}
	sample := lines[dominant.SampleLineIdx]
	slots := resolveSlots(dominant, lines)
	fields := autoFieldsFromSlots(slots, sample, dominant)
	grok := Render(sample, fields, slots)

	re, err := CompileGrok(grok, nil)
	if err != nil {
		return nil, fmt.Errorf("rendered pattern failed to compile: %w", err)
	}
	matched := EvaluateCoverage(re, lines)
	fmt.Fprintf(diag, "drain: cluster=%d matched=%d/%d\n", dominant.ID, matched, len(lines))

	return &DiscoveredPattern{
		Source:       "drain",
		SourceFamily: "drain",
		Grok:         grok,
		Coverage:     ratio(matched, len(lines)),
		MatchedCount: matched,
		TotalLines:   len(lines),
		SampleLine:   sample,
	}, nil
}

func deriveSafeFallback(lines []string) *DiscoveredPattern {
	candidates := []struct {
		Source string
		Grok   string
	}{
		{"fallback:ISO Timestamp", `%{TIMESTAMP_ISO8601:timestamp}\s+%{GREEDYDATA:message}`},
		{"fallback:Syslog Timestamp", `%{SYSLOGTIMESTAMP:timestamp}\s+%{GREEDYDATA:message}`},
		{"fallback:Message", `%{GREEDYDATA:message}`},
	}
	var best *DiscoveredPattern
	for _, c := range candidates {
		re, err := CompileGrok(c.Grok, nil)
		if err != nil {
			continue
		}
		matched := EvaluateCoverage(re, lines)
		dp := &DiscoveredPattern{
			Source:       c.Source,
			SourceFamily: "fallback",
			Grok:         c.Grok,
			Coverage:     ratio(matched, len(lines)),
			MatchedCount: matched,
			TotalLines:   len(lines),
		}
		if c.Source == "fallback:Message" {
			if best == nil {
				best = dp
			}
			break
		}
		if dp.Coverage >= 0.80 {
			return dp
		}
		best = pickBetter(best, dp)
	}
	if best == nil {
		panic("log2grok: fallback message pattern failed to compile or match")
	}
	return best
}
