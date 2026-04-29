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

func evaluateCoverageWithFloor(re *regexp.Regexp, lines []string, floor int) int {
	if re == nil {
		return 0
	}
	n := 0
	for i, line := range lines {
		if re.MatchString(line) {
			n++
		}
		if floor >= 0 && n+(len(lines)-i-1) < floor {
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
