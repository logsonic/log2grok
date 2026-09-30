package pattern

import (
	"regexp"
	"runtime"
)

// EvaluateCoverage runs re against every line; returns count of matches.
// Scans are parallel on multi-core machines for large inputs (see scan.go);
// results are identical to the sequential scan.
func EvaluateCoverage(re *regexp.Regexp, lines []string) int {
	if re == nil {
		return 0
	}
	return scanMatches(re, lines, nil)
}

// evaluateCoverageWithFloor scans lines counting matches, returning early
// if the candidate cannot strictly exceed `floor`. The caller (currently
// betterCandidate) compares with strict `>`, so we prune when the best
// achievable final count is `<= floor`. The returned partial count is
// intentionally not the true match count when pruning fires; it is only
// guaranteed to be `<= floor`. Callers MUST NOT use the return value for
// any comparison weaker than `>`, or they will rank pruned candidates
// incorrectly.
//
// On multi-core machines with large inputs the scan runs parallel and
// returns the EXACT count instead of pruning. This is decision-equivalent:
// sequential pruning only fires when the true count is <= floor (if the
// final count could exceed floor, no prefix satisfies the prune condition),
// so an exact count never flips a strict-`>` decision.
func evaluateCoverageWithFloor(re *regexp.Regexp, lines []string, floor int) int {
	return evaluateCoverageWithFloorCtl(re, lines, floor, nil)
}

// evaluateCoverageCtl is EvaluateCoverage with a cancellation token. A
// cancelled scan returns a partial count that the caller must discard —
// used by the discovery stages so a losing stage stops scanning once a
// higher-priority stage auto-accepts.
func evaluateCoverageCtl(re *regexp.Regexp, lines []string, ctl *scanCtl) int {
	return scanMatches(re, lines, ctl)
}

// evaluateCoverageWithFloorCtl is evaluateCoverageWithFloor with a
// cancellation token. On the parallel path cancellation returns a partial
// count that the caller must discard (the discovery coordinator only reads
// a stage's result when that stage was NOT aborted).
func evaluateCoverageWithFloorCtl(re *regexp.Regexp, lines []string, floor int, ctl *scanCtl) int {
	if re == nil {
		return 0
	}
	if runtime.GOMAXPROCS(0) >= 2 && len(lines) >= parallelScanMinLines {
		return scanMatches(re, lines, ctl)
	}
	n := 0
	for i, line := range lines {
		if ctl.isAborted() {
			if scanAbortHook != nil {
				scanAbortHook()
			}
			return n
		}
		if re.MatchString(line) {
			n++
		}
		if floor >= 0 && n+(len(lines)-i-1) <= floor {
			return n
		}
	}
	return n
}

func ratio(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom)
}
