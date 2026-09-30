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
// betterCandidate) compares with strict `>` on the count, but then TIE-BREAKS
// on other fields when the count equals the current best, so a candidate
// whose true count merely equals `floor` must still be represented by the
// sequential pruned partial — not by the exact count. Otherwise the parallel
// path would reach the tie-break where sequential pruning rejected the
// candidate, changing which library pattern wins (and making the result
// GOMAXPROCS-dependent). See TestEvaluateCoverageWithFloorTieMatchesSequentialDecision.
//
// Contract: the returned value is the exact count when the candidate can
// strictly beat `floor` (or when `floor < 0`); otherwise it is <= `floor` and
// is decision-equivalent to the sequential prune.
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
		exact := scanMatches(re, lines, ctl)
		// exact == floor is the only value where exact and pruned diverge in a
		// way callers observe: an exact tie reaches betterCandidate's
		// tie-break, whereas the sequential prune returns a partial <= floor
		// and is rejected on the count comparison alone. Reproduce the prune
		// for that one candidate so the decision is identical to sequential.
		if floor >= 0 && exact == floor {
			return evaluateCoverageWithFloorSeq(re, lines, floor, ctl)
		}
		return exact
	}
	return evaluateCoverageWithFloorSeq(re, lines, floor, ctl)
}

// evaluateCoverageWithFloorSeq is the original sequential floor-pruned scan.
// It returns the exact count when the candidate can still strictly exceed
// floor, and an early partial (<= floor) otherwise.
func evaluateCoverageWithFloorSeq(re *regexp.Regexp, lines []string, floor int, ctl *scanCtl) int {
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
