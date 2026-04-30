package pattern

import "regexp"

// EvaluateCoverage runs re against every line; returns count of matches.
func EvaluateCoverage(re *regexp.Regexp, lines []string) int {
	if re == nil {
		return 0
	}
	n := 0
	for _, line := range lines {
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

// evaluateCoverageWithFloor scans lines counting matches, returning early
// if the candidate cannot strictly exceed `floor`. The caller (currently
// betterCandidate) compares with strict `>`, so we prune when the best
// achievable final count is `<= floor`. The returned partial count is
// intentionally not the true match count when pruning fires; it is only
// guaranteed to be `<= floor`. Callers MUST NOT use the return value for
// any comparison weaker than `>`, or they will rank pruned candidates
// incorrectly.
func evaluateCoverageWithFloor(re *regexp.Regexp, lines []string, floor int) int {
	if re == nil {
		return 0
	}
	n := 0
	for i, line := range lines {
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
