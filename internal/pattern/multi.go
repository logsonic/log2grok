package pattern

import (
	"fmt"
	"io"
	"regexp"
)

// MultiPatternResult is what DiscoverMulti returns: an ordered set of
// standalone Grok patterns that together cover at least TargetCoverage of
// the input (or as much as the candidate pool allows). Unlike Discover's
// single alternation, each entry is an independent, first-match-wins rule
// suitable for a Logstash/Vector grok list.
//
// Patterns is ordered by selection: the first explains the most lines, and
// each subsequent one explains the most of what remained. Each pattern's
// Coverage/MatchedCount are its own standalone figures; CombinedCoverage
// and CombinedMatched describe the union of the whole set.
type MultiPatternResult struct {
	Patterns         []*DiscoveredPattern
	CombinedCoverage float64
	CombinedMatched  int
	TotalLines       int
	Estimated        bool
	EvalLines        int
}

const (
	// defaultTargetCoverage is the combined-coverage goal when the caller
	// does not set Options.TargetCoverage.
	defaultTargetCoverage = 0.90
	// maxMultiPatterns caps how many patterns the set may contain. The
	// goal is files made of fewer than ~10 distinct shapes.
	maxMultiPatterns = 10
	// multiMinGainFraction is the share of the eval set a pattern must
	// newly explain to be worth adding after the first. It stops the
	// selector from chasing the target with a long tail of overfit
	// single-line patterns: below this floor we stop and report the
	// coverage actually achieved. The first (best) pattern is always kept.
	multiMinGainFraction = 0.01
	// multiCatchallMaxSpecificity excludes the library's generic catchall
	// tier (specificity <= 20, per the library layering) from the pool: a
	// "timestamp + GREEDYDATA" rule would otherwise win round one by
	// matching everything and defeat the purpose of suggesting a set.
	multiCatchallMaxSpecificity = 20
)

// multiCandidate is one pattern in the selection pool together with its
// precomputed per-line match bitmap over the eval set, so the greedy
// selector can score marginal coverage with cheap boolean math instead of
// re-running regexes each round.
type multiCandidate struct {
	dp          *DiscoveredPattern
	matched     []bool
	count       int
	familyRank  int
	typed       int
	specificity int
}

// DiscoverMulti returns a minimal-ish set of Grok patterns whose combined
// coverage reaches opts.TargetCoverage (default 0.90). It is the
// multi-format counterpart to Discover: where Discover commits to one
// pattern (unioning clusters into an alternation when needed), DiscoverMulti
// hands back the individual patterns so each can be inspected, named, and
// maintained separately.
func DiscoverMulti(lines []string, opts Options) (*MultiPatternResult, error) {
	considered, truncated := limitLines(lines, opts.MaxLines)
	normalized := normalizeLines(considered)
	if len(normalized.MatchLines) == 0 {
		return nil, ErrEmptyInput
	}

	target := opts.TargetCoverage
	if target <= 0 {
		target = defaultTargetCoverage
	}
	if target > 1 {
		target = 1
	}
	diag := opts.Diagnostics
	if diag == nil {
		diag = io.Discard
	}

	full := normalized.MatchLines
	total := len(full)
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

	pool := buildMultiCandidates(sample, evalSet, drainSet, diag)
	if len(pool) == 0 {
		// Nothing informative to union; defer to the single-pattern path
		// (which still has its own fallback) so callers always get an answer.
		dp, err := Discover(lines, opts)
		if err != nil {
			return nil, err
		}
		return &MultiPatternResult{
			Patterns:         []*DiscoveredPattern{dp},
			CombinedCoverage: dp.Coverage,
			CombinedMatched:  dp.MatchedCount,
			TotalLines:       dp.TotalLines,
			Estimated:        dp.Estimated,
			EvalLines:        dp.EvalLines,
		}, nil
	}

	selected, coveredCount := greedyCover(pool, len(evalSet), target, diag)

	patterns := make([]*DiscoveredPattern, 0, len(selected))
	for _, c := range selected {
		c.dp.Coverage = ratio(c.count, len(evalSet))
		c.dp.MatchedCount = c.count
		c.dp.TotalLines = len(evalSet)
		patterns = append(patterns, finalize(c.dp, total, len(evalSet), estimated, truncated))
	}

	combinedCov := ratio(coveredCount, len(evalSet))
	combinedMatched := coveredCount
	totalOut := len(evalSet)
	if estimated {
		combinedMatched = int(float64(total)*combinedCov + 0.5)
		totalOut = total
	}
	fmt.Fprintf(diag, "multi: patterns=%d combined=%.3f (target=%.3f)\n",
		len(patterns), combinedCov, target)

	return &MultiPatternResult{
		Patterns:         patterns,
		CombinedCoverage: combinedCov,
		CombinedMatched:  combinedMatched,
		TotalLines:       totalOut,
		Estimated:        estimated,
		EvalLines:        len(evalSet),
	}, nil
}

// buildMultiCandidates assembles the selection pool: specific library
// matches (named, vendor-recognized), a structured probe if one fits, and
// every drain cluster rendered into its own pattern. Generic catchalls and
// the keyless JSON skeleton are excluded so the greedy selector is forced
// to pick informative shapes. Each candidate carries its match bitmap over
// evalSet; zero-match candidates are dropped.
func buildMultiCandidates(sample, evalSet, drainSet []string, diag io.Writer) []*multiCandidate {
	var pool []*multiCandidate
	seen := make(map[string]bool)

	add := func(dp *DiscoveredPattern, re *regexp.Regexp, fam, typed, spec int) {
		if dp == nil || re == nil || seen[dp.Grok] {
			return
		}
		matched, count := matchBitmap(re, evalSet)
		if count == 0 {
			return
		}
		seen[dp.Grok] = true
		pool = append(pool, &multiCandidate{
			dp:          dp,
			matched:     matched,
			count:       count,
			familyRank:  fam,
			typed:       typed,
			specificity: spec,
		})
	}

	// Library: top sample-scored candidates, minus the generic catchall tier.
	libCands := keepTopCandidates(scoreLibraryOnSample(sample), 24)
	for _, c := range libCands {
		if c.Pattern.Specificity <= multiCatchallMaxSpecificity {
			continue
		}
		dp := &DiscoveredPattern{
			Source:         "library:" + c.Pattern.Name,
			SourceFamily:   "library",
			Grok:           c.Pattern.Pattern,
			CustomPatterns: c.Pattern.CustomPatterns,
		}
		add(dp, c.Compiled, familyRank("library"), typedCaptureCount(c.Pattern.Pattern), c.Pattern.Specificity)
	}

	// Structured: a single best probe, excluding the keyless JSON skeleton.
	if s := tryStructured(sample, evalSet, io.Discard); s != nil && structuredHasTypedCapture(s) {
		if re, err := CompileGrok(s.Grok, s.CustomPatterns); err == nil {
			add(s, re, familyRank("structured"), typedCaptureCount(s.Grok), 0)
		}
	}

	// Drain: every cluster rendered into its own pattern.
	if clusters, err := trainDrain(drainSet); err == nil {
		for _, cl := range clusters {
			grok, ok := renderCluster(cl, drainSet)
			if !ok {
				continue
			}
			re, err := CompileGrok(grok, nil)
			if err != nil {
				continue
			}
			dp := &DiscoveredPattern{
				Source:       fmt.Sprintf("drain:cluster-%d", cl.ID),
				SourceFamily: "drain",
				Grok:         grok,
				SampleLine:   drainSet[cl.SampleLineIdx],
			}
			add(dp, re, familyRank("drain"), typedCaptureCount(grok), 0)
			if len(pool) >= 128 {
				break
			}
		}
	} else {
		fmt.Fprintf(diag, "multi: drain unavailable: %v\n", err)
	}

	return pool
}

// greedyCover runs weighted set-cover over the candidate pool against
// nEval lines, repeatedly taking the candidate that explains the most
// still-uncovered lines until the target fraction is reached, the pattern
// cap is hit, or no candidate adds anything. Ties in marginal gain break
// toward the more informative candidate (specific library < structured <
// drain, then more typed captures, then higher specificity), with pool
// order as the final deterministic tiebreak.
func greedyCover(pool []*multiCandidate, nEval int, target float64, diag io.Writer) ([]*multiCandidate, int) {
	covered := make([]bool, nEval)
	coveredCount := 0
	used := make([]bool, len(pool))
	targetCount := int(float64(nEval)*target + 0.999)
	minGain := int(float64(nEval)*multiMinGainFraction + 0.999)
	if minGain < 1 {
		minGain = 1
	}

	var out []*multiCandidate
	for coveredCount < targetCount && len(out) < maxMultiPatterns {
		bestIdx, bestGain := -1, 0
		for i, c := range pool {
			if used[i] {
				continue
			}
			gain := 0
			for j, m := range c.matched {
				if m && !covered[j] {
					gain++
				}
			}
			if gain == 0 {
				continue
			}
			if gain > bestGain || (gain == bestGain && bestIdx >= 0 && preferMultiCandidate(c, pool[bestIdx])) {
				bestGain, bestIdx = gain, i
			}
		}
		if bestIdx < 0 {
			break
		}
		// Always keep the first pattern; after that, stop once the best
		// remaining pattern is too marginal to be worth suggesting.
		if len(out) > 0 && bestGain < minGain {
			fmt.Fprintf(diag, "multi: stopping, best remaining gain %d < floor %d\n", bestGain, minGain)
			break
		}
		used[bestIdx] = true
		c := pool[bestIdx]
		for j, m := range c.matched {
			if m && !covered[j] {
				covered[j] = true
				coveredCount++
			}
		}
		out = append(out, c)
		fmt.Fprintf(diag, "multi: + %s gain=%d combined=%.3f\n",
			c.dp.Source, bestGain, ratio(coveredCount, nEval))
	}
	return out, coveredCount
}

// preferMultiCandidate reports whether a should beat b when their marginal
// gains tie. Lower family rank wins (specific library beats structured
// beats drain), then more typed captures, then higher specificity.
func preferMultiCandidate(a, b *multiCandidate) bool {
	if a.familyRank != b.familyRank {
		return a.familyRank < b.familyRank
	}
	if a.typed != b.typed {
		return a.typed > b.typed
	}
	return a.specificity > b.specificity
}

// matchBitmap runs re over lines, returning a per-line hit bitmap and the
// total hit count.
func matchBitmap(re *regexp.Regexp, lines []string) ([]bool, int) {
	out := make([]bool, len(lines))
	n := 0
	for i, line := range lines {
		if re.MatchString(line) {
			out[i] = true
			n++
		}
	}
	return out, n
}
