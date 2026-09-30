package pattern

import (
	"math/rand"
	"regexp"
	"runtime"
	"sync"
	"testing"
)

// deterministicLines builds n pseudo-random lines mixing digits, words and
// punctuation with a fixed seed, so counts are stable across runs.
func deterministicLines(n int) []string {
	r := rand.New(rand.NewSource(42))
	words := []string{"alpha", "beta", "gamma", "delta", "42", "7", "-", "x1"}
	lines := make([]string, n)
	for i := range lines {
		line := ""
		for k := 0; k < 6; k++ {
			line += words[r.Intn(len(words))] + " "
		}
		lines[i] = line
	}
	return lines
}

func TestScanMatchesParallelEqualsSequential(t *testing.T) {
	old := parallelScanMinLines
	parallelScanMinLines = 0 // force the parallel path
	defer func() { parallelScanMinLines = old }()

	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("single-core machine; parallel path unreachable")
	}
	re := regexp.MustCompile(`\d+`)
	for _, n := range []int{1, 2, scanChunkLines - 1, scanChunkLines, scanChunkLines + 1, 3*scanChunkLines + 17, 10000} {
		lines := deterministicLines(n)
		want := 0
		for _, line := range lines {
			if re.MatchString(line) {
				want++
			}
		}
		if got := scanMatches(re, lines, nil); got != want {
			t.Fatalf("n=%d: scanMatches=%d, want %d", n, got, want)
		}
	}
}

func TestScanMatchesSingleProc(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)
	old := parallelScanMinLines
	parallelScanMinLines = 0
	defer func() { parallelScanMinLines = old }()

	re := regexp.MustCompile(`\d+`)
	lines := deterministicLines(10000)
	want := 0
	for _, line := range lines {
		if re.MatchString(line) {
			want++
		}
	}
	if got := scanMatches(re, lines, nil); got != want {
		t.Fatalf("GOMAXPROCS=1: scanMatches=%d, want %d", got, want)
	}
}

func TestScanCtlAbort(t *testing.T) {
	ctl := newScanCtl()
	if ctl.isAborted() {
		t.Fatal("fresh ctl must not be aborted")
	}
	var nilCtl *scanCtl
	if nilCtl.isAborted() {
		t.Fatal("nil ctl must read as not aborted")
	}
	nilCtl.abort() // must not panic

	re := regexp.MustCompile(`x`)
	lines := make([]string, 20000)
	for i := range lines {
		lines[i] = "x"
	}
	ctl.abort()
	if got := scanMatches(re, lines, ctl); got != 0 {
		t.Fatalf("aborted scan must return 0 immediately, got %d", got)
	}
}

func TestScanAbortHook(t *testing.T) {
	old := parallelScanMinLines
	parallelScanMinLines = 0
	defer func() { parallelScanMinLines = old }()

	fired := make(chan struct{}, 1)
	oldHook := scanAbortHook
	scanAbortHook = func() { fired <- struct{}{} }
	defer func() { scanAbortHook = oldHook }()

	re := regexp.MustCompile(`x`)
	lines := make([]string, 20000)
	for i := range lines {
		lines[i] = "x"
	}
	ctl := newScanCtl()
	ctl.abort()
	scanMatches(re, lines, ctl)
	select {
	case <-fired:
	default:
		t.Fatal("abort hook did not fire for a cancelled scan")
	}
}

func TestMatchBitmapParallel(t *testing.T) {
	old := parallelScanMinLines
	parallelScanMinLines = 0
	defer func() { parallelScanMinLines = old }()

	re := regexp.MustCompile(`\d+`)
	lines := deterministicLines(9000)
	bitmap, count := matchBitmap(re, lines)
	want := 0
	for i, line := range lines {
		match := re.MatchString(line)
		if match {
			want++
		}
		if bitmap[i] != match {
			t.Fatalf("bitmap[%d]=%v, want %v", i, bitmap[i], match)
		}
	}
	if count != want {
		t.Fatalf("count=%d, want %d", count, want)
	}
}

// The floor variant's callers only make strict-> decisions
// (betterCandidate, relaxTail). Property: the returned value either equals
// the exact count, or is <= floor; and the decision "count > floor" must
// match the exact count's decision in both modes.
func TestEvaluateCoverageWithFloorDecisionEquivalence(t *testing.T) {
	old := parallelScanMinLines
	defer func() { parallelScanMinLines = old }()

	re := regexp.MustCompile(`\d+`)
	lines := deterministicLines(2000)
	exact := EvaluateCoverage(re, lines)

	for _, mode := range []int{0, 1 << 30} { // 0 forces parallel (when procs>=2), huge forces sequential
		parallelScanMinLines = mode
		for floor := -1; floor <= len(lines); floor++ {
			got := evaluateCoverageWithFloor(re, lines, floor)
			if got > floor && got != exact {
				t.Fatalf("mode=%d floor=%d: got %d, exact %d", mode, floor, got, exact)
			}
			decision := got > floor
			wantDecision := exact > floor
			if decision != wantDecision {
				t.Fatalf("mode=%d floor=%d: decision %v, exact-decision %v", mode, floor, decision, wantDecision)
			}
		}
	}
}

func TestScanMatchesConcurrentSafe(t *testing.T) {
	old := parallelScanMinLines
	parallelScanMinLines = 0
	defer func() { parallelScanMinLines = old }()

	re := regexp.MustCompile(`\d+`)
	lines := deterministicLines(50000)
	want := scanMatches(re, lines, nil)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := scanMatches(re, lines, nil); got != want {
				t.Errorf("concurrent scan=%d, want %d", got, want)
			}
		}()
	}
	wg.Wait()
}

// The parallel floor scan must yield the same accept/reject decision as the
// sequential prune for a candidate that exactly TIES the floor. betterCandidate
// compares Matched first and then tie-breaks on it, so returning the exact
// count in that one case would flip which library pattern wins.
func TestEvaluateCoverageWithFloorTieMatchesSequentialDecision(t *testing.T) {
	old := parallelScanMinLines
	defer func() { parallelScanMinLines = old }()
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("single-core machine; parallel path unreachable")
	}

	// 8192 lines; the 100 matching lines sit at the END, so the sequential
	// prune fires early and returns 0 (< floor) while the exact count ties floor.
	lines := make([]string, 8192)
	for i := range lines {
		lines[i] = "no"
	}
	for i := 8092; i < len(lines); i++ {
		lines[i] = "42"
	}
	re := regexp.MustCompile(`\d+`)
	const floor = 100 // == exact count

	best := &candidateResult{Matched: floor, Pattern: KnownPattern{Pattern: `%{WORD:a}`}}
	next := &candidateResult{Pattern: KnownPattern{Pattern: `%{WORD:a} %{WORD:b}`}}

	decide := func(parallel bool) (matched int, decision bool) {
		if parallel {
			parallelScanMinLines = 0
		} else {
			parallelScanMinLines = 1 << 30
		}
		next.Matched = evaluateCoverageWithFloorCtl(re, lines, floor, nil)
		return next.Matched, betterCandidate(next, best)
	}

	seqMatched, seqDecision := decide(false)
	parMatched, parDecision := decide(true)
	if seqDecision != parDecision {
		t.Fatalf("decision diverges: sequential=%v (matched=%d) parallel=%v (matched=%d), floor=%d",
			seqDecision, seqMatched, parDecision, parMatched, floor)
	}
	if parMatched != seqMatched {
		t.Fatalf("tie value diverges: sequential=%d parallel=%d, floor=%d", seqMatched, parMatched, floor)
	}
}
