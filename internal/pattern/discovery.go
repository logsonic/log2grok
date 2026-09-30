package pattern

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DiscoveredPattern is what Discover returns. ONE per call.
//
// For very large inputs (more lines than coverageEvalCap) the coverage
// figures are computed against a deterministic representative sample
// rather than every line: Estimated is then true, EvalLines reports how
// many lines were actually matched against, and MatchedCount/Coverage are
// statistical estimates extrapolated to TotalLines. For inputs at or
// below the cap the figures are exact and Estimated is false.
type DiscoveredPattern struct {
	Source         string
	SourceFamily   string
	Grok           string
	Coverage       float64
	MatchedCount   int
	TotalLines     int
	SampleLine     string
	Truncated      bool
	Estimated      bool
	EvalLines      int
	CustomPatterns map[string]string
}

// Options controls Discover's behavior.
type Options struct {
	LibraryThreshold float64
	MaxLines         int
	Verbose          bool
	Diagnostics      io.Writer
	// TargetCoverage is the combined-coverage goal for DiscoverMulti
	// (0 < t <= 1). Zero means use the default (0.90). Ignored by the
	// single-pattern Discover.
	TargetCoverage float64
}

// ErrEmptyInput is returned when the input has no non-empty lines.
var ErrEmptyInput = errors.New("log2grok: no non-empty input lines")

// Bounds that keep discovery cost independent of input size. Above these
// line counts the heavy stages operate on a deterministic representative
// sample (chooseSample) instead of every line, so a 1M-line file costs
// roughly the same as a coverageEvalCap-line file. They are vars (not
// consts) only so tests can lower them; production code never mutates
// them.
//
//   - coverageEvalCap bounds how many lines each candidate regex is run
//     against when estimating coverage. The estimate is unbiased because
//     every candidate is scored on the *same* sample, so their relative
//     ranking is preserved.
var coverageEvalCap = 50000

// stageAbortHook, if non-nil, is called with the lower stage's name each
// time the coordinator aborts that stage because a higher-priority stage
// auto-accepted. Test-only instrumentation; production code never sets it.
// Unlike scanAbortHook (which fires only when an in-flight scan happens to
// observe the cancellation), this records the coordinator's decision, so it
// is deterministic.
var stageAbortHook func(stage string)

// abortStage cancels a lower-priority stage's scans and, in tests, reports
// the decision. See the cancellation rule in Discover.
func abortStage(ctl *scanCtl, name string) {
	ctl.abort()
	if stageAbortHook != nil {
		stageAbortHook(name)
	}
}

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

	full := normalized.MatchLines
	total := len(full)

	// evalSet is what coverage is measured against. For inputs at or below
	// the cap it is the full slice and behavior is bit-for-bit identical to
	// the unsampled path.
	evalSet := full
	estimated := false
	if total > coverageEvalCap {
		evalSet = chooseSample(full, coverageEvalCap)
		estimated = true
	}

	sample := chooseSample(full, 4096)

	// The full input is no longer needed below; release it so a large input
	// can be collected while the stages run.
	dropFullInput(&full, &normalized, estimated)

	// All three stages run concurrently. Each writes its diagnostics to a
	// per-stage buffer so the merged output preserves stage-priority
	// ordering regardless of completion order.
	//
	// Auto-accept follows stage priority (structured > library > inferred),
	// not finish order: we read results in priority order and short-circuit
	// the moment a higher-priority stage clears its threshold. Lower-priority
	// goroutines still run to completion in the background — their channels
	// are buffered (capacity 1) so they exit cleanly without being read —
	// but they are cancelled first so their in-flight scans stop early.
	structuredCh := make(chan stageResult, 1)
	libraryCh := make(chan stageResult, 1)
	envelopeCh := make(chan stageResult, 1)
	ctlStructured, ctlLibrary, ctlInferred := newScanCtl(), newScanCtl(), newScanCtl()

	// Stage 1 — structured log formats such as (JSON / logfmt / CEF / W3C / CSV / TSV).
	// Auto-accepts only when the candidate has at least one typed capture,
	// which excludes the keyless JSON skeleton (\{%{GREEDYDATA:json}\}) so
	// it can't pre-empt the more informative library/inferred stages.
	go func() {
		var buf bytes.Buffer
		dp := tryStructured(sample, evalSet, &buf, ctlStructured)
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
		dp := tryLibrary(sample, evalSet, threshold, &buf, ctlLibrary)
		accept := dp != nil && dp.Coverage >= threshold
		if accept {
			fmt.Fprintf(&buf, "stage2 library auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
		}
		libraryCh <- stageResult{candidate: dp, autoAccept: accept, diag: &buf}
	}()

	// Stage 3 — inferred shapes: the brute-force primitive tiler and the
	// text-envelope probe. Both reconstruct a Grok from scratch by typing
	// the line's tokens; the tiler is the general case (population-voted
	// tiling, sub-token recovery, shape unions) and the envelope is a fast
	// timestamp/level/component template. We keep the better of the two.
	// This stage sits after the curated library so known vendor/framework
	// patterns keep priority.
	go func() {
		var buf bytes.Buffer
		dp := pickBetter(tryTiling(sample, evalSet, &buf, ctlInferred), tryTextEnvelope(sample, evalSet, &buf, ctlInferred))
		accept := dp != nil && dp.Coverage >= threshold
		if accept {
			fmt.Fprintf(&buf, "stage3 inferred auto-accept: %s coverage=%.3f\n", dp.Source, dp.Coverage)
		}
		envelopeCh <- stageResult{candidate: dp, autoAccept: accept, diag: &buf}
	}()

	// Read in priority order. Auto-accept of an earlier stage wins
	// regardless of which goroutine actually finished first. When a higher
	// stage auto-accepts, the lower stages' ctl tokens are aborted so their
	// in-flight scans stop early; their results are discarded.

	structured := <-structuredCh
	if structured.autoAccept {
		abortStage(ctlLibrary, "library")
		abortStage(ctlInferred, "inferred")
		flushDiag(diag, structured.diag)
		return finalize(structured.candidate, total, len(evalSet), estimated, truncated), nil
	}

	library := <-libraryCh
	if library.autoAccept {
		abortStage(ctlInferred, "inferred")
		flushDiag(diag, structured.diag, library.diag)
		return finalize(library.candidate, total, len(evalSet), estimated, truncated), nil
	}

	envelope := <-envelopeCh
	flushDiag(diag, structured.diag, library.diag, envelope.diag)
	if envelope.autoAccept {
		return finalize(envelope.candidate, total, len(evalSet), estimated, truncated), nil
	}

	var best *DiscoveredPattern
	best = pickBetter(best, structured.candidate)
	best = pickBetter(best, library.candidate)
	best = pickBetter(best, envelope.candidate)

	if best != nil && best.MatchedCount > 0 {
		return finalize(best, total, len(evalSet), estimated, truncated), nil
	}

	return finalize(deriveSafeFallback(evalSet), total, len(evalSet), estimated, truncated), nil
}

// finalize stamps truncation, and—when coverage was measured against a
// sample rather than the whole input—rescales the matched count to the
// true total and flags the result as an estimate. Coverage (a ratio) is
// already the sampled estimate of the true coverage, so it is preserved.
func finalize(dp *DiscoveredPattern, total, evalLines int, estimated, truncated bool) *DiscoveredPattern {
	if dp == nil {
		return nil
	}
	dp.Truncated = truncated
	dp.EvalLines = evalLines
	if estimated {
		dp.Estimated = true
		dp.TotalLines = total
		dp.MatchedCount = int(float64(total)*dp.Coverage + 0.5)
	}
	return dp
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
// in stage-priority order (structured, library, inferred), which preserves
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
	case "inferred":
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

// dropFullInput releases the package's references to the full normalized
// input once sampling is done. When the input exceeded coverageEvalCap,
// evalSet and sample are independent copies, so the full slice (which can be
// ~110 MB at 1M lines) is no longer needed and may be garbage-collected
// rather than living until Discover returns. A no-op when evalSet aliases
// full (estimated == false): the caller still owns that slice.
func dropFullInput(full *[]string, normalized *normalizedInput, estimated bool) {
	if !estimated {
		return
	}
	*full = nil
	normalized.MatchLines = nil
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

func tryStructured(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern {
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
		matched := evaluateCoverageCtl(re, all, ctl)
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

func tryLibrary(sample, all []string, threshold float64, diag io.Writer, ctl *scanCtl) *DiscoveredPattern {
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
		matched := evaluateCoverageWithFloorCtl(c.Compiled, all, floor, ctl)
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
// available, structured and inferred candidates. It is a
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

	full := normalized.MatchLines
	total := len(full)
	evalSet := full
	estimated := false
	if total > coverageEvalCap {
		evalSet = chooseSample(full, coverageEvalCap)
		estimated = true
	}

	sample := chooseSample(full, 4096)
	dropFullInput(&full, &normalized, estimated)

	// Score every library pattern on the sample, then re-evaluate the
	// top 24 on the eval set. We keep more candidates than the
	// single-best path (which uses 12) so the API can surface up to ~10
	// distinct shapes when K is large.
	candidates := scoreLibraryOnSample(sample)
	candidates = keepTopCandidates(candidates, 24)

	out := make([]*DiscoveredPattern, 0, k)
	for _, c := range candidates {
		matched := EvaluateCoverage(c.Compiled, evalSet)
		if matched == 0 {
			continue
		}
		dp := &DiscoveredPattern{
			Source:         "library:" + c.Pattern.Name,
			SourceFamily:   "library",
			Grok:           c.Pattern.Pattern,
			Coverage:       ratio(matched, len(evalSet)),
			MatchedCount:   matched,
			TotalLines:     len(evalSet),
			CustomPatterns: c.Pattern.CustomPatterns,
		}
		out = append(out, finalize(dp, total, len(evalSet), estimated, truncated))
		if len(out) >= k {
			break
		}
	}

	// If the library was thin, top up with a structured candidate.
	if len(out) < k {
		if s := tryStructured(sample, evalSet, io.Discard, nil); s != nil {
			out = append(out, finalize(s, total, len(evalSet), estimated, truncated))
		}
	}
	if len(out) < k {
		if e := pickBetter(tryTiling(sample, evalSet, io.Discard, nil), tryTextEnvelope(sample, evalSet, io.Discard, nil)); e != nil {
			out = append(out, finalize(e, total, len(evalSet), estimated, truncated))
		}
	}
	return out, nil
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
		{"fallback:HTTP Request Summary", `%{WORD:method}\s+%{URIPATHPARAM:url}\s+%{INT:status}(?:\s+%{DURATION:duration})?`},
		{"fallback:Log Level Message", `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}`},
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
