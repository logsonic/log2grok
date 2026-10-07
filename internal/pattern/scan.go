package pattern

import (
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
)

// scanChunkLines is the largest work-queue chunk for parallel scans. Workers
// claim chunks via an atomic counter, so slow chunks (backtracking
// GREEDYDATA tails) don't strand one worker while others idle: worst-case
// imbalance is bounded by one chunk.
const scanChunkLines = 1024

// scanMinChunkLines is the smallest chunk: even a cheap regexp spends tens of
// microseconds on 32 lines, well above the cost of claiming a chunk.
const scanMinChunkLines = 32

// scanChunkSize picks the chunk for an n-line parallel scan: about four
// chunks per worker so small inputs (a few hundred lines) still spread
// across every core, capped at scanChunkLines for large inputs.
func scanChunkSize(n, procs int) int {
	return min(max((n+4*procs-1)/(4*procs), scanMinChunkLines), scanChunkLines)
}

// parallelScanMinLines is the input size below which scans run sequentially:
// under ~100 lines a scan is cheaper than starting the workers. Callers such
// as logsonic send previews of a few hundred to a thousand lines, which must
// take the parallel path. Var, not const, so tests can force either path.
var parallelScanMinLines = 100

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

	chunk := scanChunkSize(n, procs)
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

// scanMatchesFloor is the parallel form of evaluateCoverageWithFloorSeq and
// returns exactly what it would. The sequential scan prunes at the line
// holding the B-th miss, where B = len(lines)-floor, returning the matches
// seen before it (i+1-B). Workers record misses per chunk and stop claiming
// chunks once B misses are known; claimed chunks always run to completion,
// so the finished chunks form a prefix and the B-th miss can be located by
// rescanning a single chunk. Without pruning, a losing candidate (most of
// the library stage's top-12 and relaxTail's cuts) would pay a full scan
// that the sequential path skips after a handful of lines.
func scanMatchesFloor(re *regexp.Regexp, lines []string, floor int, ctl *scanCtl) int {
	n := len(lines)
	budget := n - floor
	if budget <= 0 {
		// The sequential prune fires on line 0; nothing to parallelize.
		return evaluateCoverageWithFloorSeq(re, lines, floor, ctl)
	}

	procs := runtime.GOMAXPROCS(0)
	chunk := scanChunkSize(n, procs)
	chunks := (n + chunk - 1) / chunk
	workers := min(procs, chunks)
	chunkMiss := make([]int, chunks)
	var next, misses atomic.Int64
	var stop, hitCtlFlag atomic.Bool
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for !stop.Load() {
				c := int(next.Add(1)) - 1
				if c >= chunks {
					return
				}
				if ctl.isAborted() {
					hitCtlFlag.Store(true)
					stop.Store(true)
					return
				}
				start := c * chunk
				miss := 0
				for _, line := range lines[start:min(start+chunk, n)] {
					if !re.MatchString(line) {
						miss++
					}
				}
				chunkMiss[c] = miss
				if misses.Add(int64(miss)) >= int64(budget) {
					stop.Store(true)
				}
			}
		}()
	}
	wg.Wait()
	if hitCtlFlag.Load() {
		if scanAbortHook != nil {
			scanAbortHook()
		}
		return 0 // partial; the caller discards an aborted stage's result
	}

	// Chunks [0, claimed) all completed; walk them in order to find the
	// chunk holding the budget-th miss.
	claimed := min(int(next.Load()), chunks)
	seen := 0
	for c := 0; c < claimed; c++ {
		if seen+chunkMiss[c] < budget {
			seen += chunkMiss[c]
			continue
		}
		start := c * chunk
		for i := start; i < min(start+chunk, n); i++ {
			if !re.MatchString(lines[i]) {
				seen++
				if seen == budget {
					return i + 1 - budget
				}
			}
		}
	}
	// Fewer than budget misses overall: the candidate beats floor and every
	// chunk was scanned, so the exact count is n - misses.
	return n - seen
}
