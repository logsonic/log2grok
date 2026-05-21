package pattern

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
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
//   - drainTrainCap bounds how many lines are fed to drain3 for template
//     learning. Templates converge well before this many lines; feeding
//     more only grows memory and CPU.
var (
	coverageEvalCap = 50000
	drainTrainCap   = 20000
)

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

	// evalSet is what coverage is measured against; drainSet is what drain
	// trains on. For inputs at or below the caps these are the full slice
	// and behavior is bit-for-bit identical to the unsampled path.
	evalSet := full
	estimated := false
	if total > coverageEvalCap {
		evalSet = chooseSample(full, coverageEvalCap)
		estimated = true
	}
	drainSet := full
	if total > drainTrainCap {
		drainSet = chooseSample(full, drainTrainCap)
	}

	sample := chooseSample(full, 4096)

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
		dp := tryStructured(sample, evalSet, &buf)
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
		dp := tryLibrary(sample, evalSet, threshold, &buf)
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
		dp, err := deriveFromDrain(drainSet, evalSet, &buf)
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
		return finalize(structured.candidate, total, len(evalSet), estimated, truncated), nil
	}

	library := <-libraryCh
	if library.autoAccept {
		flushDiag(diag, structured.diag, library.diag)
		return finalize(library.candidate, total, len(evalSet), estimated, truncated), nil
	}

	drain := <-drainCh
	flushDiag(diag, structured.diag, library.diag, drain.diag)
	if drain.autoAccept {
		return finalize(drain.candidate, total, len(evalSet), estimated, truncated), nil
	}

	var best *DiscoveredPattern
	best = pickBetter(best, structured.candidate)
	best = pickBetter(best, library.candidate)
	if drain.candidate != nil && drain.candidate.Grok != "" {
		best = pickBetter(best, drain.candidate)
	}

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

	full := normalized.MatchLines
	total := len(full)
	evalSet := full
	estimated := false
	if total > coverageEvalCap {
		evalSet = chooseSample(full, coverageEvalCap)
		estimated = true
	}

	sample := chooseSample(full, 4096)

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
		if s := tryStructured(sample, evalSet, io.Discard); s != nil {
			out = append(out, finalize(s, total, len(evalSet), estimated, truncated))
		}
	}
	return out, nil
}

// deriveFromDrain learns templates from trainLines (a bounded sample of
// the input) and measures the resulting Grok against evalLines (the
// coverage sample, which equals the full input when it is small enough).
// Separating the two keeps drain3's clustering cost bounded on huge
// inputs while still scoring coverage on a representative population.
func deriveFromDrain(trainLines, evalLines []string, diag io.Writer) (*DiscoveredPattern, error) {
	clusters, err := trainDrain(trainLines)
	if err != nil {
		return nil, err
	}
	if len(clusters) == 0 {
		return nil, errors.New("drain produced no clusters")
	}
	dominant := clusters[0]
	if dominant.SampleLineIdx < 0 || dominant.SampleLineIdx >= len(trainLines) {
		return nil, errors.New("drain dominant cluster has no representative line")
	}
	sample := trainLines[dominant.SampleLineIdx]
	slots := resolveSlots(dominant, trainLines)
	fields := autoFieldsFromSlots(slots, sample, dominant)
	grok := Render(sample, fields, slots)

	re, err := CompileGrok(grok, nil)
	if err != nil {
		return nil, fmt.Errorf("rendered pattern failed to compile: %w", err)
	}
	matched := EvaluateCoverage(re, evalLines)
	cov := ratio(matched, len(evalLines))
	fmt.Fprintf(diag, "drain: cluster=%d matched=%d/%d clusters=%d\n",
		dominant.ID, matched, len(evalLines), len(clusters))

	// When the dominant cluster alone leaves a lot of lines unmatched but
	// the input is made of only a handful of distinct shapes, union the
	// top clusters into a single alternation so multi-format logs are
	// detected instead of collapsing to a generic fallback.
	if cov < multiPatternMinCoverage && len(clusters) >= 2 {
		if multi := deriveMultiPattern(clusters, trainLines, evalLines, cov, diag); multi != nil {
			return multi, nil
		}
	}

	// Reject severely overfit patterns: if Drain produced a pattern that
	// matches fewer than ~20% of lines, it's almost certainly literal text
	// that won't generalize. Fall through to fallback instead.
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
		TotalLines:   len(evalLines),
		SampleLine:   sample,
	}, nil
}

// Multi-pattern tuning. These only take effect when the dominant drain
// cluster is a poor fit for the whole input, so single-format logs (where
// the dominant cluster already matches ~everything) never reach this code.
var (
	// multiPatternMinCoverage is the dominant-cluster coverage below which
	// we consider unioning multiple clusters.
	multiPatternMinCoverage = 0.85
	// multiPatternMaxClusters bounds how many distinct shapes we treat as
	// a "small number of patterns" worth unioning. The goal is files made
	// of fewer than ~10 formats.
	multiPatternMaxClusters = 10
	// multiPatternMinGain is the absolute coverage improvement the union
	// must deliver over the dominant cluster alone to be worth returning.
	multiPatternMinGain = 0.15
	// multiPatternMinCombined is the floor the unioned coverage must clear
	// before we prefer it over the normal single-cluster / fallback path.
	multiPatternMinCombined = 0.90
)

// deriveMultiPattern handles inputs composed of a small number (fewer than
// multiPatternMaxClusters) of distinct line shapes. It renders each of the
// top clusters into its own Grok body and greedily unions the branches
// that add coverage into a single alternation, so multi-format logs are
// detected as one combined pattern instead of collapsing to a generic
// GREEDYDATA fallback. Returns nil (caller falls back to the dominant
// handling) unless the union both clears multiPatternMinCombined coverage
// and improves on the dominant cluster by at least multiPatternMinGain.
func deriveMultiPattern(clusters []cluster, trainLines, evalLines []string, dominantCov float64, diag io.Writer) *DiscoveredPattern {
	if len(clusters) < 2 || len(clusters) > multiPatternMaxClusters {
		return nil
	}

	// Track which eval lines remain unmatched so each added branch is only
	// credited for the new lines it explains.
	remaining := make([]bool, len(evalLines))
	for i := range remaining {
		remaining[i] = true
	}

	type branch struct {
		grok string
		re   *regexp.Regexp
	}
	var branches []branch
	for _, c := range clusters {
		grok, ok := renderCluster(c, trainLines)
		if !ok {
			continue
		}
		re, err := CompileGrok(grok, nil)
		if err != nil {
			continue
		}
		gained := 0
		for i, line := range evalLines {
			if remaining[i] && re.MatchString(line) {
				remaining[i] = false
				gained++
			}
		}
		if gained == 0 {
			continue
		}
		branches = append(branches, branch{grok: grok, re: re})
	}
	if len(branches) < 2 {
		return nil
	}

	parts := make([]string, 0, len(branches))
	for _, b := range branches {
		parts = append(parts, "(?:"+b.grok+")")
	}
	unionGrok := "(?:" + strings.Join(parts, "|") + ")"

	unionRe, err := CompileGrok(unionGrok, nil)
	if err != nil {
		fmt.Fprintf(diag, "drain multi: union failed to compile: %v\n", err)
		return nil
	}
	matched := EvaluateCoverage(unionRe, evalLines)
	cov := ratio(matched, len(evalLines))
	fmt.Fprintf(diag, "drain multi: branches=%d matched=%d/%d coverage=%.3f (dominant=%.3f)\n",
		len(branches), matched, len(evalLines), cov, dominantCov)

	if cov < multiPatternMinCombined || cov-dominantCov < multiPatternMinGain {
		return nil
	}
	return &DiscoveredPattern{
		Source:       fmt.Sprintf("drain:multi(%d)", len(branches)),
		SourceFamily: "drain",
		Grok:         unionGrok,
		Coverage:     cov,
		MatchedCount: matched,
		TotalLines:   len(evalLines),
		SampleLine:   trainLines[clusters[0].SampleLineIdx],
	}
}

// renderCluster renders a single drain cluster into a Grok body using its
// representative line. Returns ok=false when the cluster has no usable
// representative line.
func renderCluster(c cluster, trainLines []string) (string, bool) {
	if c.SampleLineIdx < 0 || c.SampleLineIdx >= len(trainLines) {
		return "", false
	}
	sample := trainLines[c.SampleLineIdx]
	slots := resolveSlots(c, trainLines)
	fields := autoFieldsFromSlots(slots, sample, c)
	return Render(sample, fields, slots), true
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
