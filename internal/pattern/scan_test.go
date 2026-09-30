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
