package pattern

import (
	"fmt"
	"strings"
	"testing"
)

// withCaps temporarily lowers the sampling caps so large-input behavior
// can be exercised without generating millions of lines, restoring the
// originals when the test finishes.
func withCaps(t *testing.T, evalCap, drainCap int) {
	t.Helper()
	oe, od := coverageEvalCap, drainTrainCap
	coverageEvalCap, drainTrainCap = evalCap, drainCap
	t.Cleanup(func() { coverageEvalCap, drainTrainCap = oe, od })
}

func genNginxLines(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf(
			`10.0.0.%d - alice [15/Jan/2025:10:23:%02d +0000] "GET /index%d.html HTTP/1.1" 200 %d "https://ref/%d" "Mozilla/5.0"`,
			i%256, i%60, i%1000, 100+i%9000, i%50)
	}
	return out
}

// TestLargeInputEstimatesCoverage verifies that above the coverage cap the
// result is flagged Estimated, EvalLines reports the sampled population,
// and MatchedCount is extrapolated to the full total while the Grok stays
// exact.
func TestLargeInputEstimatesCoverage(t *testing.T) {
	withCaps(t, 2000, 1000)
	const n = 20000
	lines := genNginxLines(n)

	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !dp.Estimated {
		t.Fatalf("expected Estimated=true for %d lines over cap %d", n, coverageEvalCap)
	}
	if dp.EvalLines != coverageEvalCap {
		t.Fatalf("EvalLines = %d, want %d", dp.EvalLines, coverageEvalCap)
	}
	if dp.TotalLines != n {
		t.Fatalf("TotalLines = %d, want %d", dp.TotalLines, n)
	}
	if dp.MatchedCount != n {
		t.Fatalf("MatchedCount = %d, want %d (full coverage extrapolated)", dp.MatchedCount, n)
	}
	if !strings.Contains(dp.Source, "Nginx") {
		t.Fatalf("source = %q, want a Nginx library match", dp.Source)
	}
	// The discovered pattern must genuinely match every line, not just the
	// sample it was scored against.
	re, err := CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := EvaluateCoverage(re, lines); got != n {
		t.Fatalf("recompiled coverage %d/%d", got, n)
	}
}

// TestSmallInputIsExact confirms that at or below the cap nothing is
// sampled: Estimated is false and EvalLines equals the line count.
func TestSmallInputIsExact(t *testing.T) {
	lines := genNginxLines(50)
	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if dp.Estimated {
		t.Fatalf("small input should not be estimated")
	}
	if dp.EvalLines != 50 {
		t.Fatalf("EvalLines = %d, want 50", dp.EvalLines)
	}
}

// TestMultiPatternUnion verifies that an input made of a small number of
// distinct templates—none recognized by the library—is detected as a
// single union pattern instead of collapsing to a GREEDYDATA fallback.
func TestMultiPatternUnion(t *testing.T) {
	var lines []string
	for i := 0; i < 90; i++ {
		switch i % 3 {
		case 0:
			lines = append(lines, fmt.Sprintf("CONN open peer=node%d port=%d", i%10, 8000+i))
		case 1:
			lines = append(lines, fmt.Sprintf("TASK done job-%d in %dms", i, i*7))
		case 2:
			lines = append(lines, fmt.Sprintf("CACHE evicted key%d size=%d", i%50, i*3))
		}
	}

	dp, err := Discover(lines, Options{LibraryThreshold: 0.85})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.HasPrefix(dp.Source, "drain:multi(") {
		t.Fatalf("source = %q, want a drain:multi union", dp.Source)
	}
	if dp.Coverage < 0.99 {
		t.Fatalf("union coverage = %.3f, want ~1.0", dp.Coverage)
	}
	re, err := CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		t.Fatalf("compile union: %v", err)
	}
	if got := EvaluateCoverage(re, lines); got != len(lines) {
		t.Fatalf("union matched %d/%d lines", got, len(lines))
	}
}

// TestSingleFormatDoesNotUnion guards the multi-pattern gate: a clean
// single-format log must not be turned into an alternation.
func TestSingleFormatDoesNotUnion(t *testing.T) {
	lines := genNginxLines(200)
	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if strings.Contains(dp.Source, "multi") {
		t.Fatalf("single-format log unexpectedly unioned: %q", dp.Source)
	}
}
