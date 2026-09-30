package pattern

import (
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
)

// scanChunkLines is the work-queue chunk size for parallel scans. Workers
// claim chunks via an atomic counter, so slow chunks (backtracking
// GREEDYDATA tails) don't strand one worker while others idle: worst-case
// imbalance is bounded by one chunk.
const scanChunkLines = 1024

// parallelScanMinLines is the input size below which scans run sequentially:
// goroutine spawn overhead (~2µs each, ×GOMAXPROCS) exceeds the scan cost for
// small inputs, and the per-case benchmark inputs (tens–hundreds of lines)
// must not regress. Var, not const, so tests can force the parallel path.
var parallelScanMinLines = 4096

// scanAbortHook, if non-nil, is called once per scan that exits early because
// its scanCtl was aborted. Test-only instrumentation: production code never
// sets it. Set BEFORE starting the scan that should observe it (tests set it
// before calling Discover; the goroutine-start edges make that race-free).
var scanAbortHook func()

// scanCtl lets a caller cancel a scan whose result it no longer needs
// (a higher-priority discovery stage auto-accepted). A cancelled scan
// returns a PARTIAL count that must be discarded — never ranked, never
// reported. A nil *scanCtl is valid and never aborts.
type scanCtl struct {
	aborted atomic.Bool
}

func newScanCtl() *scanCtl { return &scanCtl{} }

func (c *scanCtl) abort() {
	if c != nil {
		c.aborted.Store(true)
	}
}

func (c *scanCtl) isAborted() bool {
	return c != nil && c.aborted.Load()
}

// scanMatches counts the lines matching re. It is the single scan primitive
// for the whole package: every hot loop that counts regexp matches over a
// line slice goes through here so parallelism and cancellation behave
// uniformly.
func scanMatches(re *regexp.Regexp, lines []string, ctl *scanCtl) int {
	return scanMatchesInto(re, lines, nil, ctl)
}

// scanMatchesInto is scanMatches with an optional per-line bitmap: when
// bitmap is non-nil it must have len(lines) entries and bitmap[i] records
// whether lines[i] matched. Workers write disjoint index ranges, so the
// shared bitmap needs no locking.
//
// hitCtlFlag is written by workers (atomic: several workers may observe the
// abort) and read by the parent only after wg.Wait(), which orders it.
func scanMatchesInto(re *regexp.Regexp, lines []string, bitmap []bool, ctl *scanCtl) int {
	n := len(lines)
	if re == nil || n == 0 {
		return 0
	}
	procs := runtime.GOMAXPROCS(0)
	if procs < 2 || n < parallelScanMinLines {
		return scanMatchesSeq(re, lines, bitmap, ctl)
	}

	chunk := scanChunkLines
	workers := procs
	if w := (n + chunk - 1) / chunk; w < workers {
		workers = w
	}
	var next, total atomic.Int64
	var hitCtlFlag atomic.Bool
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			local := 0
			for {
				start := int(next.Add(int64(chunk))) - chunk
				if start >= n {
					break
				}
				if ctl.isAborted() {
					hitCtlFlag.Store(true)
					break
				}
				end := min(start+chunk, n)
				for j, line := range lines[start:end] {
					if re.MatchString(line) {
						local++
						if bitmap != nil {
							bitmap[start+j] = true
						}
					}
				}
			}
			if local > 0 {
				total.Add(int64(local))
			}
		}()
	}
	wg.Wait()
	if hitCtlFlag.Load() && scanAbortHook != nil {
		scanAbortHook()
	}
	return int(total.Load())
}

func scanMatchesSeq(re *regexp.Regexp, lines []string, bitmap []bool, ctl *scanCtl) int {
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
			if bitmap != nil {
				bitmap[i] = true
			}
		}
	}
	return n
}
