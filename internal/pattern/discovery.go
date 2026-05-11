package pattern

import (
	"bytes"
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

	// All three stages run concurrently. Each writes its diagnostics to a
	// per-stage buffer so the merged output preserves the historical
	// stage1→stage2→stage3 ordering regardless of completion order.
	//
	// Auto-accept follows stage priority (structured > library > drain),
	// not finish order: we read results in priority order and short-circuit
	// the moment a higher-priority stage clears its threshold. Lower-priority
	// goroutines still run to completion in the background — their channels
	// are buffered so they exit cleanly without being read. drain3 has no
	// in-flight cancellation hook, so this is the most we can do.
	// Buffered (capacity 1) so a goroutine whose result the coordinator
	// never reads — e.g. drain when structured auto-accepts — still
	// completes its send and exits cleanly.
	structuredCh := make(chan stageResult, 1)
	libraryCh := make(chan stageResult, 1)
	drainCh := make(chan stageResult, 1)

	// Stage 1 — structured log formats such as (JSON / logfmt / CEF / W3C / CSV / TSV).
	// Auto-accepts only when the candidate has at least one typed capture,
	// which excludes the keyless JSON skeleton (\{%{GREEDYDATA:json}\}) so
	// it can't pre-empt the more informative library/drain stages.
	go func() {
		var buf bytes.Buffer
		dp := tryStructured(sample, normalized.MatchLines, &buf)
		accept := dp != nil && dp.Coverage >= threshold && structuredHasTypedCapture(dp)
		if accept {
			fmt.Fprintf(&buf, "stage1 structured auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
		}
		structuredCh <- stageResult{candidate: dp, autoAccept: accept, diag: &buf}
	}()

	// Stage 2 — library: regex KnownPatterns scored on the sample, then
	// the top candidates re-evaluated against the full input.
	go func() {
		var buf bytes.Buffer
		dp := tryLibrary(sample, normalized.MatchLines, threshold, &buf)
		accept := dp != nil && dp.Coverage >= threshold
		if accept {
			fmt.Fprintf(&buf, "stage2 library auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
		}
		libraryCh <- stageResult{candidate: dp, autoAccept: accept, diag: &buf}
	}()

	// Stage 3 — drain: drain3 clustering → token classifier → renderer.
	// The most expensive stage and not interruptible (drain3 has no
	// cancellation hook), so this goroutine always runs to completion
	// even when a higher-priority stage has already auto-accepted.
	go func() {
		var buf bytes.Buffer
		dp, err := deriveFromDrain(normalized.MatchLines, &buf)
		if err != nil {
			fmt.Fprintf(&buf, "stage3 drain error: %v\n", err)
			drainCh <- stageResult{diag: &buf}
			return
		}
		accept := dp != nil && dp.Grok != "" && dp.Coverage >= 0.85
		if accept {
			fmt.Fprintf(&buf, "stage3 drain auto-accept: coverage=%.3f\n", dp.Coverage)
		}
		drainCh <- stageResult{candidate: dp, autoAccept: accept, diag: &buf}
	}()

	// Read in priority order. Auto-accept of an earlier stage wins
	// regardless of which goroutine actually finished first.

	structured := <-structuredCh
	if structured.autoAccept {
		flushDiag(diag, structured.diag)
		return markTruncated(structured.candidate, truncated), nil
	}

	library := <-libraryCh
	if library.autoAccept {
		flushDiag(diag, structured.diag, library.diag)
		return markTruncated(library.candidate, truncated), nil
	}

	drain := <-drainCh
	flushDiag(diag, structured.diag, library.diag, drain.diag)
	if drain.autoAccept {
		return markTruncated(drain.candidate, truncated), nil
	}

	var best *DiscoveredPattern
	best = pickBetter(best, structured.candidate)
	best = pickBetter(best, library.candidate)
	if drain.candidate != nil && drain.candidate.Grok != "" {
		best = pickBetter(best, drain.candidate)
	}

	if best != nil && best.MatchedCount > 0 {
		return markTruncated(best, truncated), nil
	}

	return markTruncated(deriveSafeFallback(normalized.MatchLines), truncated), nil
}

// stageResult carries one stage's outcome back to Discover. autoAccept
// is the per-stage auto-accept decision (already gated by stage-specific
// rules like structuredHasTypedCapture); diag is the buffered diagnostic
// stream for that stage, flushed in priority order by the coordinator.
type stageResult struct {
	candidate  *DiscoveredPattern
	autoAccept bool
	diag       *bytes.Buffer
}

// flushDiag writes per-stage diagnostic buffers to the user-supplied
// writer in the order given by the caller. Callers always pass buffers
// in stage-priority order (structured, library, drain), which preserves
// the historical "stage1 → stage2 → stage3" output shape regardless of
// which goroutine finished first. The io.Discard short-circuit avoids
// touching buffers that no one will read.
func flushDiag(w io.Writer, bufs ...*bytes.Buffer) {
	if w == nil || w == io.Discard {
		return
	}
	for _, b := range bufs {
		if b == nil || b.Len() == 0 {
			continue
		}
		_, _ = w.Write(b.Bytes())
	}
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

// structuredHasTypedCapture decides whether the structured-stage
// candidate is informative enough to auto-accept. CSV/TSV/W3C and other
// schema-driven literal patterns intentionally have zero named captures
// (column semantics are not recoverable from a delimiter); they remain
// eligible. The single case we want to block is the keyless JSON
// skeleton — `\{%{GREEDYDATA:json}\}` — which is emitted when no JSON
// key crosses the common-frequency bar. That candidate matches every
// JSON line at 100% coverage and would otherwise short-circuit the
// later (more informative) stages.
func structuredHasTypedCapture(dp *DiscoveredPattern) bool {
	if dp == nil {
		return false
	}
	if dp.Grok == `\{%{GREEDYDATA:json}\}` {
		return false
	}
	return true
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

	// Tie the per-candidate sample-coverage floor to the user's overall
	// threshold (with a hard cap at 0.50 to keep the legacy default
	// behaviour). Users who lower LibraryThreshold for heterogeneous
	// inputs get a correspondingly relaxed floor.
	sampleFloor := threshold * 0.6
	if sampleFloor > 0.50 {
		sampleFloor = 0.50
	}
	if sampleFloor < 0.10 {
		sampleFloor = 0.10
	}

	var best *candidateResult
	for _, c := range candidates {
		if c.SampleCoverage < sampleFloor {
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

// DiscoverTopK returns the top K library candidates plus, when
// available, a structured candidate and the drain candidate. It is a
// lighter-weight cousin of Discover: useful for UIs that want to
// surface "the top 3 patterns matching this log" instead of a single
// answer. Patterns are returned in descending preference order using
// the same comparator as the single-pattern stage.
//
// When K <= 0 the function uses a default of 5. The returned slice
// will have at most K entries and may be shorter (or empty if no
// library entry produced any match against the sample).
func DiscoverTopK(lines []string, k int, opts Options) ([]*DiscoveredPattern, error) {
	if k <= 0 {
		k = 5
	}
	considered, truncated := limitLines(lines, opts.MaxLines)
	normalized := normalizeLines(considered)
	if len(normalized.MatchLines) == 0 {
		return nil, ErrEmptyInput
	}

	sample := chooseSample(normalized.MatchLines, 4096)

	// Score every library pattern on the sample, then re-evaluate the
	// top 24 on the full input. We keep more candidates than the
	// single-best path (which uses 12) so the API can surface up to ~10
	// distinct shapes when K is large.
	candidates := scoreLibraryOnSample(sample)
	candidates = keepTopCandidates(candidates, 24)

	out := make([]*DiscoveredPattern, 0, k)
	for _, c := range candidates {
		matched := EvaluateCoverage(c.Compiled, normalized.MatchLines)
		if matched == 0 {
			continue
		}
		dp := &DiscoveredPattern{
			Source:         "library:" + c.Pattern.Name,
			SourceFamily:   "library",
			Grok:           c.Pattern.Pattern,
			Coverage:       ratio(matched, len(normalized.MatchLines)),
			MatchedCount:   matched,
			TotalLines:     len(normalized.MatchLines),
			CustomPatterns: c.Pattern.CustomPatterns,
		}
		out = append(out, markTruncated(dp, truncated))
		if len(out) >= k {
			break
		}
	}

	// If the library was thin, top up with a structured candidate.
	if len(out) < k {
		if s := tryStructured(sample, normalized.MatchLines, io.Discard); s != nil {
			out = append(out, markTruncated(s, truncated))
		}
	}
	return out, nil
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

	// Reject severely overfit patterns: if Drain produced a pattern that
	// matches fewer than ~20% of lines, it's almost certainly literal text
	// that won't generalize. Fall through to fallback instead.
	cov := ratio(matched, len(lines))
	if cov < 0.20 {
		fmt.Fprintf(diag, "drain: skipping overfit pattern (coverage %.3f < 0.20)\n", cov)
		return nil, nil
	}

	return &DiscoveredPattern{
		Source:       "drain",
		SourceFamily: "drain",
		Grok:         grok,
		Coverage:     cov,
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
		// Specific timestamp formats first (narrower = fewer false positives)
		{"fallback:US Date", `%{DATE_US:date} - %{TIME:time}: %{GREEDYDATA:message}`},
		{"fallback:Bracketed Date", `\[%{DATE:date} %{TIME:time}\] %{GREEDYDATA:message}`},
		{"fallback:Bracketed Time", `\[%{TIME:time}\] %{GREEDYDATA:message}`},
		{"fallback:Day Syslog", `%{DAY:day} %{SYSLOGTIMESTAMP:timestamp} %{YEAR:year} %{GREEDYDATA:message}`},
		{"fallback:Syslog Timestamp", `%{SYSLOGTIMESTAMP:timestamp}\s+%{GREEDYDATA:message}`},
		{"fallback:Date Time", `%{DATE:date} %{TIME:time} %{GREEDYDATA:message}`},
		{"fallback:ISO Timestamp", `%{TIMESTAMP_ISO8601:timestamp}\s+%{GREEDYDATA:message}`},
		// Last resort
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
			// Prefer the message catch-all over any lower-coverage
			// candidates set by earlier fallback attempts.
			if best == nil || best.MatchedCount < matched {
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
