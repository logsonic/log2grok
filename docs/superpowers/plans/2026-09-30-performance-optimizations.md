# log2grok Performance Optimizations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut `Discover` wall time ~3–8× on multi-core machines and peak memory ~5× on very large inputs, without changing any discovery result.

**Architecture:** ~88% of CPU is single-threaded `regexp` scanning (`EvaluateCoverage` 51%, `evaluateCoverageWithFloor` 34% of profile). This plan adds a GOMAXPROCS-aware, cancellation-aware scan layer (`internal/pattern/scan.go`) and rewires every hot scan through it, then fixes four secondary hot spots (unpruned `relaxTail` scans, library deep-clone on cache hit, mutex-guarded composite lookups, repeated JSON parsing) and releases the full input after sampling. Every change is behavior-preserving: same Grok, same Coverage, same MatchedCount on every input.

**Tech Stack:** Go 1.25.5 (std lib only — `go.sum` is empty and must stay that way). Go 1.25's `runtime.GOMAXPROCS(0)` is cgroup-aware, which is what makes CPU-count-based worker sizing correct in containers.

**Spec:** This plan is self-contained; the evidence section below is the spec. Background docs: `SPEC.md`, `design.md`, `README.md` (behavior contract: "one log file in, one Grok pattern out", coverage figures exact at or below `coverageEvalCap`).

## Measured baseline (Apple M5 Max, 18 cores, Go 1.25.5 — re-capture on your machine in Task 0)

| Benchmark | Result |
|---|---|
| `BenchmarkDiscoverByCase/*` (100+ typical cases) | 0.4–0.95 ms/op, 2–3 MB/op, 10k–24k allocs/op |
| `BenchmarkDiscoverScale/lines=10000` | 157 ms/op, 23 MB, 343k allocs |
| `BenchmarkDiscoverScale/lines=100000` | 279 ms/op, 32 MB, 343k allocs |
| `BenchmarkDiscoverScale/lines=1000000` | 286 ms/op, **111 MB**, 343k allocs |
| `BenchmarkDiscoverMultiScale/lines=1000000` | 420 ms/op, **132 MB**, 814k allocs |

CPU profile of `DiscoverScale/lines=100000`:

```
regexp.(*machine).add/step/match        ~88% of CPU (regexp execution)
pattern.EvaluateCoverage                50.8% cum
  <- buildTiling                        26.8%   (16 shapes × full eval scan)
  <- scoreLibraryOnSample               22.9%   (90 patterns × 4096-line sample)
  <- inferTextEnvelope                   3.4%
pattern.evaluateCoverageWithFloor       33.8%   (top-12 library re-eval on 50k eval set)
runtime.goschedImpl/gopreempt_m         ~6%     (scheduling overhead)
```

Targets (from profile shares): 1M-line `Discover` 286 ms → <100 ms; typical per-case benchmarks unchanged (they stay sequential by design); 1M-line peak retention ~111 MB → ~30 MB in library+API use.

## Global Constraints

- **Zero new dependencies.** `go.mod` declares only the module; `go.sum` is empty. Std lib only.
- **Bit-for-bit identical results.** For every input, `Discover`, `DiscoverTopK`, and `DiscoverMulti` must return the same `Grok`, `Coverage`, `MatchedCount`, `Source`, and `CustomPatterns` as before this plan. The 100+ case correctness suite (`TestDiscoverCorrectnessSuite`) is the gate.
- **Determinism.** Integer match counts are order-independent, so parallel partial sums preserve rankings exactly. Never introduce float reordering or map-iteration-order-dependent output.
- **`GOMAXPROCS=1` must take the sequential path** with zero goroutine spawns — single-core machines and CI must not regress.
- **Never mutate caller-owned slices.** `lines []string` passed to exported APIs is read-only; parallel workers only read their own chunk. Bitmap writes go to slices this package allocated.
- **Existing test-hook convention:** package-level tuning knobs are `var` (not `const`) so tests can lower them (see `coverageEvalCap`, `tilingMinLines`). Follow the same convention for new thresholds.
- Run every test step with `-race` at least once (Task 1 and Task 11) — this plan adds concurrency.

## Review Focus

Input classes / failure modes the spec implies but no single task's happy path exercises, most likely first:

1. **Tiny inputs (< `parallelScanMinLines`) under a lowered threshold** — must still produce correct counts when forced parallel (tests lower the knob); a wrong chunk-boundary index (`start >= n`) must not panic or double-count. Pinned by `TestScanMatchesParallelEqualsSequential` (Task 1).
2. **`GOMAXPROCS=1`** — no parallel fan-out, no regression vs today. Pinned by `TestScanMatchesSingleProc` (Task 1) and the Task 11 benchmark matrix.
3. **Concurrent `ReplaceKnownPatternsLibrary` / `ReplaceGrokPrimitives` during an in-flight scan** — must not race or use stale regexes. Pinned by `TestScanMatchesConcurrentSafe` (Task 1), existing `concurrency_test.go`, and `-race` (Task 11).
4. **`evaluateCoverageWithFloor` partial-count contract** — callers (`betterCandidate`, `relaxTail`) only make strict-`>` decisions; the parallel path returns the exact count, which must never flip a decision the sequential prune would have made. Pinned by `TestEvaluateCoverageWithFloorDecisionEquivalence` (Task 2).
5. **Aborted-stage partial results must never leak into output** — a scan cancelled via `scanCtl` returns a partial count that is only safe to discard; if a higher-priority stage did not auto-accept, the lower stage's ctl must NOT have been aborted. Pinned by `TestScanCtlAbort` and `TestDiscoverNoAbortWithoutAutoAccept` (Tasks 1 and 5).

---

### Task 0: Capture the baseline

**Files:**
- Create: nothing (results go in the commit message and Task 11 notes)

**Interfaces:**
- Consumes: existing benchmark suite
- Produces: baseline numbers for this machine, used by Task 11's comparison

- [ ] **Step 1: Run the full benchmark suite and save the output**

```bash
go test -bench=. -benchmem -run='^$' ./test/benchmark/ | tee /tmp/opencode/bench-before.txt
```

Expected: completes (takes ~4 min); save the `BenchmarkDiscoverScale` and `BenchmarkDiscoverByCase` lines.

- [ ] **Step 2: Record the baseline in the Task 11 verification notes**

Paste the `BenchmarkDiscoverScale/*` and `BenchmarkDiscoverMultiScale/*` lines into the Task 11 step before starting Task 1.

- [ ] **Step 3: Commit (nothing to commit — this task only produces local evidence)**

Skip the commit; verify `git status` is clean.

---

### Task 1: Parallel scan infrastructure (`scan.go`)

**Files:**
- Create: `internal/pattern/scan.go`
- Test: `internal/pattern/scan_test.go`

**Interfaces:**
- Consumes: nothing new (`regexp`, `runtime`, `sync`, `sync/atomic`)
- Produces (used by Tasks 2, 3, 5, 6):
  - `type scanCtl struct { ... }` with `func newScanCtl() *scanCtl`, `func (c *scanCtl) abort()`, `func (c *scanCtl) isAborted() bool` (nil receiver is valid and never aborted)
  - `func scanMatches(re *regexp.Regexp, lines []string, ctl *scanCtl) int` — counts matching lines; parallel when `runtime.GOMAXPROCS(0) >= 2 && len(lines) >= parallelScanMinLines`; aborts early when `ctl` fires (partial count, discard-only)
  - `func scanMatchesInto(re *regexp.Regexp, lines []string, bitmap []bool, ctl *scanCtl) int` — same, plus fills `bitmap[i]` per line (bitmap may be nil)
  - Vars: `parallelScanMinLines int` (default 4096, tests may lower), `const scanChunkLines = 1024`
  - Test hook: `var scanAbortHook func()` — called once per scan that exits early due to abort (test-only)

- [ ] **Step 1: Write the failing tests**

Create `internal/pattern/scan_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run 'TestScanMatches|TestScanCtl|TestScanAbort' ./internal/pattern/`
Expected: FAIL — `undefined: scanMatches` (and friends).

- [ ] **Step 3: Write the implementation**

Create `internal/pattern/scan.go`:

```go
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
```

Note the ordering subtlety: the abort check happens per chunk (every `scanChunkLines` lines), not per line — an atomic load per line would cost more than the match itself on cheap lines. A worker that already claimed a chunk finishes it before observing the abort; that is fine, because a cancelled scan's count is discarded by contract.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestScanMatches|TestScanCtl|TestScanAbort' ./internal/pattern/ -v`
Expected: all PASS.

- [ ] **Step 5: Run the whole package suite (nothing else may break)**

Run: `go test ./internal/pattern/ ./pkg/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pattern/scan.go internal/pattern/scan_test.go
git commit -m "perf: add GOMAXPROCS-aware cancellable scan primitive (scan.go)"
```

---

### Task 2: Route `EvaluateCoverage` and `evaluateCoverageWithFloor` through `scan.go`

**Files:**
- Modify: `internal/pattern/coverage.go` (whole file, 48 lines)
- Test: `internal/pattern/scan_test.go` (add equivalence test)

**Interfaces:**
- Consumes: `scanMatches` (Task 1)
- Produces: unchanged public signatures — `EvaluateCoverage(re *regexp.Regexp, lines []string) int`, `evaluateCoverageWithFloor(re *regexp.Regexp, lines []string, floor int) int`. The floor variant's doc contract changes: the parallel path returns the EXACT count (not a pruned partial). Callers are safe because the existing contract only permits strict-`>` comparisons, and exact counts satisfy every decision a pruned count could.

- [ ] **Step 1: Write the failing test**

Add to `internal/pattern/scan_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestEvaluateCoverageWithFloorDecisionEquivalence ./internal/pattern/ -v`
Expected: PASS already? No — it must fail only if the wiring is wrong; since the function still exists with old semantics the property may hold. The real gate is Step 4 (suite green) plus this property staying green after the rewrite. If it passes pre-rewrite, that's fine — proceed; the property test's value is regression protection, not red-green.

- [ ] **Step 3: Rewrite `coverage.go`**

Replace the whole of `internal/pattern/coverage.go` with:

```go
package pattern

import "regexp"

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
	if re == nil {
		return 0
	}
	if runtime.GOMAXPROCS(0) >= 2 && len(lines) >= parallelScanMinLines {
		return scanMatches(re, lines, nil) // exact count
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
```

Note: add `"runtime"` to the import block.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run 'Test'`
Expected: PASS — including `TestDiscoverCorrectnessSuite` (100+ cases must produce byte-identical groks).

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/coverage.go internal/pattern/scan_test.go
git commit -m "perf: route EvaluateCoverage/evaluateCoverageWithFloor through scan.go"
```

---

### Task 3: Parallel `matchBitmap` (multi.go)

**Files:**
- Modify: `internal/pattern/multi.go:296-308` (`matchBitmap`)
- Test: `internal/pattern/scan_test.go` (add test)

**Interfaces:**
- Consumes: `scanMatchesInto` (Task 1)
- Produces: unchanged `matchBitmap(re *regexp.Regexp, lines []string) ([]bool, int)` signature; internally cancellation-capable is NOT added here (multi has no auto-accept path — pass nil ctl).

- [ ] **Step 1: Write the failing test**

Add to `internal/pattern/scan_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it passes/fails appropriately**

Run: `go test -race -run TestMatchBitmapParallel ./internal/pattern/ -v`
Expected: PASS (old sequential implementation is correct) — this is a regression guard; the win is verified by benchmark in Task 11.

- [ ] **Step 3: Rewrite `matchBitmap`**

In `internal/pattern/multi.go`, replace:

```go
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
```

with:

```go
// matchBitmap runs re over lines, returning a per-line hit bitmap and the
// total hit count. The scan is parallel for large inputs (see scan.go);
// workers write disjoint bitmap ranges, so no locking is needed.
func matchBitmap(re *regexp.Regexp, lines []string) ([]bool, int) {
	out := make([]bool, len(lines))
	n := scanMatchesInto(re, lines, out, nil)
	return out, n
}
```

- [ ] **Step 4: Run the multi tests**

Run: `go test -race -run 'TestMatchBitmapParallel|Multi' ./internal/pattern/ ./test/benchmark/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/multi.go internal/pattern/scan_test.go
git commit -m "perf: parallel matchBitmap via scanMatchesInto"
```

---

### Task 4: Floor-prune `relaxTail`'s cut-point scans

**Files:**
- Modify: `internal/pattern/tiler.go:1024-1069` (`relaxTail`)
- Test: `internal/pattern/tiler_test.go` (add test)

**Interfaces:**
- Consumes: `evaluateCoverageWithFloor` (Task 2 — exact counts on the parallel path keep accepted candidates' `Matched` exact)
- Produces: unchanged `relaxTail(best *tileCandidate, eval []string) *tileCandidate` signature and behavior.

- [ ] **Step 1: Write the failing/characterization test**

Add to `internal/pattern/tiler_test.go` (adapt names to what already compiles there — read the file first; the assertion is that a relaxed tiling for a rigid-prefix log is still found):

```go
// relaxTail must still find the relaxation that buys real coverage for a
// rigid-prefix/varying-tail log, now that cut-point scans are floor-pruned.
func TestRelaxTailFindsCoverageGain(t *testing.T) {
	eval := []string{
		"2025-01-15T10:23:45Z INFO worker started",
		"2025-01-15T10:23:46Z INFO job 1 done",
		"2025-01-15T10:23:47Z INFO job 2 done",
		"2025-01-15T10:23:48Z INFO shutdown",
		"2025-01-15T10:23:49Z INFO bye",
	}
	sample := eval
	shapes := tileShapes(sample, eval, tileShapeTemplates)
	best := bestTiling(shapes, eval)
	if best == nil {
		t.Fatal("no tiling")
	}
	relaxed := relaxTail(best, eval)
	// Either no relaxation buys >=2% (acceptable), or the relaxed candidate
	// strictly beats best and carries a GREEDYDATA message tail.
	if relaxed != nil {
		if relaxed.Matched <= best.Matched {
			t.Fatalf("relaxed=%d must beat best=%d", relaxed.Matched, best.Matched)
		}
		if !strings.Contains(relaxed.Grok, "%{GREEDYDATA:message}") {
			t.Fatalf("relaxed grok lacks message tail: %s", relaxed.Grok)
		}
	}
}
```

(`strings` is already imported in `tiler_test.go`; verify before compiling.)

- [ ] **Step 2: Run test to verify it passes pre-change (characterization)**

Run: `go test -run TestRelaxTailFindsCoverageGain ./internal/pattern/ -v`
Expected: PASS (characterization). If it fails, fix the test to a case that passes — the point is a stable assertion to preserve across the rewrite.

- [ ] **Step 3: Apply the floor-pruned evaluation**

In `internal/pattern/tiler.go` inside `relaxTail`, replace:

```go
		matched := EvaluateCoverage(re, eval)
		if matched-best.Matched < margin {
			continue
		}
```

with:

```go
		// Floor-prune: a cut that cannot strictly beat best.Matched+margin
		// bails out mid-scan instead of completing a full coverage pass.
		// Accepted candidates always carry exact counts (pruning only fires
		// when the true count is <= floor; see coverage.go).
		matched := evaluateCoverageWithFloor(re, eval, best.Matched+margin-1)
		if matched-best.Matched < margin {
			continue
		}
```

- [ ] **Step 4: Run tiling + correctness suites**

Run: `go test -run 'TestRelaxTail|Tiling' ./internal/pattern/ -v && go test ./test/benchmark/ -run TestDiscoverCorrectnessSuite`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/tiler.go internal/pattern/tiler_test.go
git commit -m "perf: floor-prune relaxTail cut-point coverage scans"
```

---

### Task 5: Cancel losing stages on auto-accept

**Files:**
- Modify: `internal/pattern/discovery.go` (`Discover`, stage goroutines, `tryStructured`, `tryLibrary`, `tryTiling`, `tryTextEnvelope` signatures) and `internal/pattern/tiler.go` (`buildTiling`, `bestTiling`, `relaxTail`, `tiledUnion` signatures), `internal/pattern/text_envelope.go` (`tryTextEnvelope` body), `internal/pattern/score.go` (`tryLibrary` body's eval call), `internal/pattern/multi.go` (`buildMultiCandidates` call sites)
- Test: `internal/pattern/discovery_test.go` (add test)

**Interfaces:**
- Consumes: `scanCtl` (Task 1)
- Produces (new internal signatures — all unexported, single package):
  - `tryStructured(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern`
  - `tryLibrary(sample, all []string, threshold float64, diag io.Writer, ctl *scanCtl) *DiscoveredPattern`
  - `tryTiling(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern`
  - `tryTextEnvelope(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern`
  - `buildTiling(template string, sample, eval []string, ctl *scanCtl) *tileCandidate`
  - `bestTiling(shapes []*tileCandidate, eval []string, ctl *scanCtl) *tileCandidate`
  - `relaxTail(best *tileCandidate, eval []string, ctl *scanCtl) *tileCandidate`
  - `tiledUnion(shapes []*tileCandidate, eval []string, bestCov float64, diag io.Writer, ctl *scanCtl) *DiscoveredPattern`
  - Helper: `func evaluateCoverageCtl(re *regexp.Regexp, lines []string, ctl *scanCtl) int { return scanMatches(re, lines, ctl) }` in coverage.go
  - Cancellation rule: a stage's ctl is aborted ONLY after a strictly-higher-priority stage auto-accepts (structured → aborts library+inferred; library → aborts inferred). Aborted results are always discarded because the auto-accept path returns before reading them.

- [ ] **Step 1: Write the failing test**

Add to `internal/pattern/discovery_test.go`:

```go
// When stage 1 (structured) auto-accepts, the library and inferred stages
// must be aborted (their scans stop early). The result must still be the
// stage-1 candidate, byte-identical to the pre-cancellation behavior.
func TestDiscoverAbortsLowerStagesOnStructuredAutoAccept(t *testing.T) {
	oldCap := coverageEvalCap
	coverageEvalCap = 100000 // keep the exact-coverage path
	defer func() { coverageEvalCap = oldCap }()

	lines := []string{
		`{"ts":"2025-01-15T10:23:45Z","level":"info","msg":"job 1"}`,
		`{"ts":"2025-01-15T10:23:46Z","level":"info","msg":"job 2"}`,
		`{"ts":"2025-01-15T10:23:47Z","level":"info","msg":"job 3"}`,
	}
	opts := Options{LibraryThreshold: 0.85}

	fired := make(chan struct{}, 8)
	oldHook := scanAbortHook
	scanAbortHook = func() { fired <- struct{}{} }
	defer func() { scanAbortHook = oldHook }()

	dp, err := Discover(lines, opts)
	if err != nil {
		t.Fatal(err)
	}
	if dp.SourceFamily != "structured" {
		t.Fatalf("expected structured auto-accept, got %s (%s)", dp.SourceFamily, dp.Source)
	}
	select {
	case <-fired:
		// at least one lower-priority scan aborted — good
	default:
		t.Fatal("no scan aborted; lower stages ran to completion after structured auto-accept")
	}
}

// And the inverse: with NO auto-accept, no stage may be aborted (aborted
// partial counts would poison pickBetter).
func TestDiscoverNoAbortWithoutAutoAccept(t *testing.T) {
	oldCap := coverageEvalCap
	coverageEvalCap = 100000
	defer func() { coverageEvalCap = oldCap }()

	// Heterogeneous lines: no stage reaches the 0.85 threshold.
	lines := []string{
		"user alice logged in from 10.0.0.1",
		"backup finished with status 0",
		"cache: 512 entries evicted",
		"temperature reading 21.5 celsius",
	}
	opts := Options{LibraryThreshold: 0.999}

	aborted := make(chan struct{}, 8)
	oldHook := scanAbortHook
	scanAbortHook = func() { aborted <- struct{}{} }
	defer func() { scanAbortHook = oldHook }()

	if _, err := Discover(lines, opts); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
		t.Fatal("a stage was aborted although no stage auto-accepted")
	default:
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestDiscoverAbortsLowerStages|TestDiscoverNoAbort' ./internal/pattern/ -v`
Expected: `TestDiscoverAbortsLowerStagesOnStructuredAutoAccept` FAILS ("no scan aborted"); `TestDiscoverNoAbortWithoutAutoAccept` PASSES (nothing aborts yet).

- [ ] **Step 3: Thread ctl through the stage functions**

3a. In `internal/pattern/discovery.go`, add the helper to `coverage.go` instead:

```go
// evaluateCoverageCtl is EvaluateCoverage with a cancellation token. A
// cancelled scan returns a partial count that the caller must discard —
// used by the discovery stages so a losing stage stops scanning once a
// higher-priority stage auto-accepts.
func evaluateCoverageCtl(re *regexp.Regexp, lines []string, ctl *scanCtl) int {
	return scanMatches(re, lines, ctl)
}
```

3b. In `discovery.go` `Discover`, give each stage its own ctl and wire aborts:

```go
	structuredCh := make(chan stageResult, 1)
	libraryCh := make(chan stageResult, 1)
	envelopeCh := make(chan stageResult, 1)
	ctlStructured, ctlLibrary, ctlInferred := newScanCtl(), newScanCtl(), newScanCtl()
```

Stage goroutines pass their ctl: `tryStructured(sample, evalSet, &buf, ctlStructured)`, `tryLibrary(sample, evalSet, threshold, &buf, ctlLibrary)`, and inside stage 3: `pickBetter(tryTiling(sample, evalSet, &buf, ctlInferred), tryTextEnvelope(sample, evalSet, &buf, ctlInferred))`.

In the priority-order read section, abort lower stages exactly when a higher one auto-accepts:

```go
	structured := <-structuredCh
	if structured.autoAccept {
		ctlLibrary.abort()
		ctlInferred.abort()
		flushDiag(diag, structured.diag)
		return finalize(structured.candidate, total, len(evalSet), estimated, truncated), nil
	}

	library := <-libraryCh
	if library.autoAccept {
		ctlInferred.abort()
		flushDiag(diag, structured.diag, library.diag)
		return finalize(library.candidate, total, len(evalSet), estimated, truncated), nil
	}
```

3c. Apply the mechanical rewrites (each function gains `ctl *scanCtl` as its LAST parameter; each internal full-eval call switches to the ctl variant):

- `tryStructured`: `matched := EvaluateCoverage(re, all)` → `matched := evaluateCoverageCtl(re, all, ctl)`
- `tryLibrary`: `matched := evaluateCoverageWithFloor(c.Compiled, all, floor)` → `matched := evaluateCoverageWithFloorCtl(c.Compiled, all, floor, ctl)` — add to coverage.go:

```go
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
```

  (Then simplify `evaluateCoverageWithFloor` to delegate: `return evaluateCoverageWithFloorCtl(re, lines, floor, nil)`.)
- `tryTiling` → `tileShapes` → `buildTiling`: add `ctl` parameter down the chain; `buildTiling`'s `matched := EvaluateCoverage(re, eval)` → `evaluateCoverageCtl(re, eval, ctl)`.
- `bestTiling` → `relaxTail`: add `ctl`; `relaxTail`'s scan (Task 4's floor-pruned call) becomes `evaluateCoverageWithFloorCtl(re, eval, best.Matched+margin-1, ctl)`.
- `tiledUnion`: add `ctl`; its `matched := EvaluateCoverage(re, eval)` → `evaluateCoverageCtl(re, eval, ctl)`. Its per-line `remaining` loop stays sequential (it maintains cross-candidate state; out of scope).
- `tryTextEnvelope`: add `ctl`; the full-eval call `matched := EvaluateCoverage(re, all)` → `evaluateCoverageCtl(re, all, ctl)`. The 4096-line sample scan stays uncancellable (cheap).
- `multi.go` `buildMultiCandidates` call sites: pass `nil` for all three try* calls and `tileShapes` (multi has no auto-accept priority order; it needs every candidate).
- Test call sites: the new signatures break earlier tasks' tests that call stage functions directly — update `tiler_test.go` `TestRelaxTailFindsCoverageGain` (`relaxTail(best, eval)` → `relaxTail(best, eval, nil)`, and `bestTiling(shapes, eval)` → `bestTiling(shapes, eval, nil)`, `tileShapes(sample, eval, tileShapeTemplates)` → `tileShapes(sample, eval, tileShapeTemplates, nil)`) and any other direct stage-function callers found via `go build ./...` errors. Pass `nil` in all tests (nil ctl = never aborts, matching pre-Task-5 behavior).

- [ ] **Step 4: Run the discovery + correctness suites**

Run: `go test -race -run 'TestDiscover' ./internal/pattern/ -v && go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run Test`
Expected: PASS, including both new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/
git commit -m "perf: abort lower-priority discovery stages once a higher stage auto-accepts"
```

---

### Task 6: Parallelize `scoreLibraryOnSample` across patterns

**Files:**
- Modify: `internal/pattern/score.go:95-108` (`scoreLibraryOnSample`)
- Test: `internal/pattern/score_test.go` (create)

**Interfaces:**
- Consumes: `EvaluateCoverage` (Task 2 — each per-pattern sample scan is ≥`parallelScanMinLines`? No: sample is 4096 = exactly at the threshold boundary; the OUTER loop parallelizes, inner scans stay sequential to avoid 90×18 oversubscription)
- Produces: unchanged `scoreLibraryOnSample(sample []string) []candidateResult` signature; results indexed identically and sorted afterwards by existing callers.

- [ ] **Step 1: Write the failing test**

Create `internal/pattern/score_test.go`:

```go
package pattern

import "testing"

// scoreLibraryOnSample must return the same sample coverage for every
// pattern whether computed sequentially or with the outer parallel loop.
func TestScoreLibraryOnSampleParallelEquivalence(t *testing.T) {
	sample := deterministicLines(300)
	seq := scoreLibraryOnSample(sample) // current implementation
	// The parallel rewrite keeps the signature; re-run and compare.
	par := scoreLibraryOnSample(sample)
	if len(seq) != len(par) {
		t.Fatalf("len mismatch: %d vs %d", len(seq), len(par))
	}
	for i := range seq {
		if seq[i].SampleCoverage != par[i].SampleCoverage || seq[i].Matched != par[i].Matched {
			t.Fatalf("pattern %d (%s): seq=%d par=%d", i, seq[i].Pattern.Name, seq[i].Matched, par[i].Matched)
		}
	}
}
```

- [ ] **Step 2: Run test (characterization)**

Run: `go test -run TestScoreLibraryOnSampleParallelEquivalence ./internal/pattern/ -v`
Expected: PASS (same function twice). Regression guard.

- [ ] **Step 3: Parallelize the outer loop**

Replace `scoreLibraryOnSample` in `internal/pattern/score.go`:

```go
// scoreLibraryOnSample scores every compiled library pattern against the
// sample. The outer loop parallelizes across patterns (bounded by
// GOMAXPROCS); each per-pattern scan stays sequential so parallelism is
// not nested 90×GOMAXPROCS deep. Results keep input order (indexed writes).
func scoreLibraryOnSample(sample []string) []candidateResult {
	compiled := compiledKnownPatterns()
	out := make([]candidateResult, len(compiled))
	workers := min(runtime.GOMAXPROCS(0), len(compiled))
	if workers < 2 {
		for i, cp := range compiled {
			out[i] = scoreOne(cp, sample)
		}
		return out
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(compiled) {
					return
				}
				out[i] = scoreOne(compiled[i], sample)
			}
		}()
	}
	wg.Wait()
	return out
}

func scoreOne(cp compiledPattern, sample []string) candidateResult {
	matched := EvaluateCoverage(cp.Regex, sample)
	return candidateResult{
		Pattern:        cp.Pattern,
		Compiled:       cp.Regex,
		SampleCoverage: ratio(matched, len(sample)),
		Matched:        matched,
	}
}
```

Add `"runtime"`, `"sync"`, `"sync/atomic"` to score.go's imports (it already imports `sync`).

- [ ] **Step 4: Run tests + race**

Run: `go test -race -run 'TestScoreLibrary' ./internal/pattern/ -v && go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run Test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/score.go internal/pattern/score_test.go
git commit -m "perf: parallelize library sample scoring across patterns"
```

---

### Task 7: Stop deep-cloning the library on every cache hit

**Files:**
- Modify: `internal/pattern/state.go:39-43` (add `knownPatternsSnapshot`), `internal/pattern/score.go:36-75` (`compiledKnownPatterns`)
- Test: `internal/pattern/library_test.go` (add test)

**Interfaces:**
- Consumes: existing `currentPatternStateVersion()`
- Produces: `func knownPatternsSnapshot() []KnownPattern` (deep clone, rebuild path only). `knownPatternsSnapshotWithVersion` is deleted — grep for other callers first (`grep -rn knownPatternsSnapshotWithVersion --include='*.go' .`): expected sole caller is `compiledKnownPatterns`; delete it if so.

- [ ] **Step 1: Write the failing test**

Add to `internal/pattern/library_test.go`:

```go
// After a library replacement, the compiled cache must reflect the new
// library on the next call (version-bump path — the path that clones).
func TestCompiledCacheTracksLibraryReplacement(t *testing.T) {
	orig := KnownPatternsLibrarySnapshot()
	defer commitPatternStateForTest(orig) // restore; helper below

	replacement := []KnownPattern{{
		Name:    "Test Only Unique Pattern",
		Pattern: `^UNIQUEMARKER (?P<value>\w+)$`,
	}}
	ReplaceKnownPatternsLibrary(replacement)

	compiled := compiledKnownPatterns()
	found := false
	for _, cp := range compiled {
		if cp.Pattern.Name == "Test Only Unique Pattern" {
			found = true
		}
	}
	if !found {
		t.Fatal("compiled cache did not pick up replacement library")
	}
}

func commitPatternStateForTest(orig []KnownPattern) {
	prims := GrokPrimitivesSnapshot()
	commitPatternState(prims, orig, nil)
}
```

(Read `library_test.go` first — if a restore helper already exists, use it.)

- [ ] **Step 2: Run test to verify it passes pre-change (characterization)**

Run: `go test -run TestCompiledCacheTracksLibraryReplacement ./internal/pattern/ -v`
Expected: PASS. (This pins the behavior the refactor must not break; the win is allocation reduction, verified in Task 11.)

- [ ] **Step 3: Defer the clone to the rebuild path**

In `internal/pattern/state.go`, add next to `knownPatternsSnapshotWithVersion`:

```go
// knownPatternsSnapshot returns a deep copy of the active library. Unlike
// the old snapshotWithVersion, it is only called on cache-rebuild paths —
// the cache-hit path reads the version alone, without cloning 90 patterns
// and their CustomPatterns maps on every Discover call.
func knownPatternsSnapshot() []KnownPattern {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return cloneKnownPatterns(KnownPatterns)
}
```

In `internal/pattern/score.go` `compiledKnownPatterns`, replace:

```go
	for {
		version, patterns := knownPatternsSnapshotWithVersion()

		compileMu.Lock()
		if compiledLib != nil && compiledVersion == version {
			out := append([]compiledPattern(nil), compiledLib...)
			compileMu.Unlock()
			return out
		}
		compileMu.Unlock()
```

with:

```go
	for {
		version := currentPatternStateVersion()

		compileMu.Lock()
		if compiledLib != nil && compiledVersion == version {
			out := append([]compiledPattern(nil), compiledLib...)
			compileMu.Unlock()
			return out
		}
		compileMu.Unlock()

		// Rebuild path only: clone the library now. This preserves the
		// existing (pre-existing) benign race window — a bump between the
		// version read and the rebuild check discards the snapshot, same
		// as before.
		patterns := knownPatternsSnapshot()
```

Delete `knownPatternsSnapshotWithVersion` from state.go if (and only if) the grep in Interfaces found no other callers.

- [ ] **Step 4: Run library + concurrency + full suites**

Run: `go test -race -run 'Library|Compiled' ./internal/pattern/ -v && go test ./internal/pattern/ ./pkg/... -run Test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/state.go internal/pattern/score.go internal/pattern/library_test.go
git commit -m "perf: skip library deep-clone on compiled-cache hits"
```

---

### Task 8: Precompile tiler composite expressions (remove the global mutex)

**Files:**
- Modify: `internal/pattern/tiler.go:93-127` (`tileExprRe` cache, `exprFullMatch`), `tiler.go:131-151` (`matchComposite`), `tiler.go:98-109` (`resetTilerCaches`)
- Test: `internal/pattern/tiler_test.go` (add test)

**Interfaces:**
- Consumes: `CompileGrok`, `currentPatternStateVersion` (for version-checked lazy build, mirroring `ensureTileSingles`)
- Produces: `exprFullMatch` is DELETED (grep first: sole caller is `matchComposite` — verified by grep on 2026-09-30). Composite `Expr` regexes live in a version-checked slice `compExprRe []*regexp.Regexp` parallel to `tileComposites` (nil entry = failed compile = never matches).

- [ ] **Step 1: Write the failing test**

Add to `internal/pattern/tiler_test.go`:

```go
// Composite expression regexes must be rebuilt when the primitive table is
// replaced (resetTilerCaches), not cached forever.
func TestCompositeExprsTrackPrimitiveReplacement(t *testing.T) {
	if len(compExprRe) == 0 {
		t.Fatal("compExprRe not built")
	}
	// ISO composite must be present and usable.
	re := compExprRe[1] // index 1 = bare ISO8601 composite
	if re == nil {
		t.Fatal("ISO8601 composite expr failed to compile")
	}
	if !re.MatchString("2025-01-15T10:23:45Z") {
		t.Fatal("ISO8601 composite does not match an ISO timestamp")
	}

	// Force a rebuild path by resetting caches; must still work.
	resetTilerCaches()
	ensureCompExprs()
	if len(compExprRe) != len(tileComposites) {
		t.Fatalf("compExprRe len %d != tileComposites %d", len(compExprRe), len(tileComposites))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCompositeExprsTrackPrimitiveReplacement ./internal/pattern/ -v`
Expected: FAIL — `undefined: compExprRe`.

- [ ] **Step 3: Implement the version-checked precompile**

In `internal/pattern/tiler.go`, replace the `tileExprRe` cache block (lines 93-127) with:

```go
var (
	compExprMu     sync.RWMutex
	compExprVer    uint64
	compExprRe     []*regexp.Regexp // parallel to tileComposites; nil = compile failed
	compExprLoaded bool
)

// ensureCompExprs compiles every composite's Expr once per primitive-table
// version. Called lazily (primitives may load after tiler var init) and on
// the hot path of matchComposite — RLock-only after first build.
func ensureCompExprs() {
	version := currentPatternStateVersion()
	compExprMu.RLock()
	ok := compExprLoaded && compExprVer == version
	compExprMu.RUnlock()
	if ok {
		return
	}

	built := make([]*regexp.Regexp, len(tileComposites))
	for i, c := range tileComposites {
		re, err := CompileGrok(c.Expr, nil)
		if err != nil {
			re = nil // failed composites never match (old exprFullMatch semantics)
		}
		built[i] = re
	}

	compExprMu.Lock()
	defer compExprMu.Unlock()
	if compExprLoaded && compExprVer == version {
		return
	}
	compExprRe = built
	compExprVer = version
	compExprLoaded = true
}
```

Update `resetTilerCaches` — replace the `tileExprMu` block:

```go
	compExprMu.Lock()
	compExprVer = 0
	compExprRe = nil
	compExprLoaded = false
	compExprMu.Unlock()
```

Update `matchComposite` — index-based access instead of the string-keyed mutex map:

```go
func matchComposite(line string, i int) (*tileComposite, int) {
	ensureCompExprs()
	compExprMu.RLock()
	defer compExprMu.RUnlock()

	rest := line[i:]
	for ci, c := range tileComposites {
		loc := c.prefix.FindStringIndex(rest)
		if loc == nil || loc[1] == 0 {
			continue
		}
		end := i + loc[1]
		if end < len(line) && !tileDelims[line[end]] && !tileSubSeps[line[end]] {
			continue
		}
		if compExprRe[ci] == nil || !compExprRe[ci].MatchString(rest[:loc[1]]) {
			continue
		}
		return c, loc[1]
	}
	return nil, 0
}
```

Delete `exprFullMatch` (verify sole caller with `grep -rn exprFullMatch --include='*.go' .` first; the grep must show only tiler.go's definition and matchComposite call).

- [ ] **Step 4: Run tiler + full suites with race**

Run: `go test -race -run 'Tile|Composite|Segment' ./internal/pattern/ -v && go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run Test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/tiler.go internal/pattern/tiler_test.go
git commit -m "perf: precompile tiler composite exprs, drop per-token mutex map"
```

---

### Task 9: Release the full input after sampling

**Files:**
- Modify: `internal/pattern/discovery.go` (`Discover` ~line 91, `DiscoverTopK` ~line 444), `internal/pattern/multi.go` (`DiscoverMulti` ~line 94), `cmd/log2grok/main.go` (`main` after discovery calls)
- Test: `internal/pattern/discovery_test.go` (add behavior test)

**Interfaces:**
- Consumes: existing sampling (`chooseSample`, `coverageEvalCap`)
- Produces: no signature changes. Memory contract: when `estimated == true` (input exceeded the cap), `Discover` no longer keeps the full input reachable; only `evalSet` (≤50k lines) + `sample` (≤4096) are retained.

- [ ] **Step 1: Write the characterization test**

Add to `internal/pattern/discovery_test.go`:

```go
// Estimated (sampled) results must keep their extrapolated figures after
// the full input is released — the release must not change any output.
func TestEstimatedResultUnchangedAfterFullRelease(t *testing.T) {
	oldCap := coverageEvalCap
	coverageEvalCap = 64
	defer func() { coverageEvalCap = oldCap }()

	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, `10.0.0.7 - alice [15/Jan/2025:10:23:45 +0000] "GET /index.html HTTP/1.1" 200 1024`)
	}
	dp, err := Discover(lines, Options{LibraryThreshold: 0.85})
	if err != nil {
		t.Fatal(err)
	}
	if !dp.Estimated {
		t.Fatal("expected estimated result with lowered cap")
	}
	if dp.TotalLines != 200 || dp.MatchedCount <= 0 || dp.Coverage <= 0 {
		t.Fatalf("bad extrapolated figures: %+v", dp)
	}
	if dp.Grok == "" {
		t.Fatal("empty grok")
	}
}
```

- [ ] **Step 2: Run test (characterization)**

Run: `go test -run TestEstimatedResultUnchangedAfterFullRelease ./internal/pattern/ -v`
Expected: PASS pre-change (pins the output contract).

- [ ] **Step 3: Release the reference after sampling**

In `internal/pattern/discovery.go` `Discover`, after the `sample := chooseSample(full, 4096)` line, insert:

```go
	if estimated {
		// The full input is no longer needed: only total (captured above),
		// evalSet and sample are used below. Drop the references so a
		// 1M-line input is garbage-collected instead of living until return
		// (~110 MB measured at 1M lines). No-op when evalSet aliases full.
		full = nil
		normalized.MatchLines = nil
	}
```

Apply the identical insertion in `DiscoverTopK` (after its `sample := chooseSample(...)` line) and in `multi.go` `DiscoverMulti` (after its `sample := ...` line).

In `cmd/log2grok/main.go`, after the discovery call returns and before printing, drop the caller's reference too (the CLI holds `lines` until main returns otherwise). In `main`, after the single-pattern branch computes `dp` (and in `runMulti` after `DiscoverMulti` returns), the slices are still referenced by `main`'s `lines` variable; add `lines = nil` immediately after the `discoverLines(...)` / `runMulti(...)` call succeeds:

```go
	dp, err := discoverLines(lines, truncated, *threshold, *verbose, diag)
	lines = nil // discovery sampled internally; release the full input
	if err != nil {
```

and in the multi branch:

```go
	if *multi {
		err := runMulti(lines, *threshold, *target, *quiet, *verbose, diag)
		lines = nil
		if err != nil {
```

(Adjust to keep `err` handling identical — assign to the existing `err` variable, don't shadow.)

- [ ] **Step 4: Run full suites**

Run: `go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run Test && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pattern/discovery.go internal/pattern/multi.go cmd/log2grok/main.go internal/pattern/discovery_test.go
git commit -m "perf: release full input after sampling (peak memory at 1M lines)"
```

---

### Task 10: Parse JSON sample lines once (shared across all structured probes)

**Files:**
- Create: `internal/pattern/json_sample.go`
- Modify: `internal/pattern/identify.go` — `structuredProbe` type (lines 11-15), every probe's `Likely`/`Render`, `jsonObjectFraction`, `keyFreq`, `observeJSONShape`, `parseJSONLine` callers; `internal/pattern/discovery.go` (`tryStructured` builds the cache)
- Test: `internal/pattern/json_sample_test.go` (create)

**Interfaces:**
- Consumes: existing `parseJSONLine(line string) (map[string]any, bool)` (unchanged, stays as the single-line parser)
- Produces:
  - `type jsonSample struct { ... }` with `func newJSONSample(lines []string) *jsonSample`, `func (js *jsonSample) Lines() []string`, `func (js *jsonSample) Obj(i int) (map[string]any, bool)` (memoized), `func (js *jsonSample) Count() int`
  - `structuredProbe.Likely` becomes `func(js *jsonSample) bool`; `structuredProbe.Render` becomes `func(js *jsonSample) (grok string, source string, ok bool)`
  - `tryStructured` constructs ONE `jsonSample` per call and passes it to every probe — each sample line is parsed at most once per `Discover` instead of ~6-9 times.

- [ ] **Step 1: Write the failing test**

Create `internal/pattern/json_sample_test.go`:

```go
package pattern

import "testing"

func TestJSONSampleMemoizes(t *testing.T) {
	lines := []string{
		`{"a":1}`,
		`not json`,
		`{"b":"two"}`,
		`{"a":1}`, // duplicate content, separate parse (index-based memo)
	}
	js := newJSONSample(lines)
	if js.Count() != 4 {
		t.Fatalf("count=%d", js.Count())
	}
	obj, ok := js.Obj(0)
	if !ok || obj["a"] != float64(1) {
		t.Fatalf("Obj(0)=%v ok=%v", obj, ok)
	}
	if _, ok := js.Obj(1); ok {
		t.Fatal("Obj(1) should not parse")
	}
	obj2, ok := js.Obj(2)
	if !ok || obj2["b"] != "two" {
		t.Fatalf("Obj(2)=%v ok=%v", obj2, ok)
	}
	// Memoized re-access returns the same map.
	objAgain, ok := js.Obj(0)
	if !ok || objAgain["a"] != float64(1) {
		t.Fatal("memoized re-access broken")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestJSONSampleMemoizes ./internal/pattern/ -v`
Expected: FAIL — `undefined: newJSONSample`.

- [ ] **Step 3: Implement `json_sample.go`**

Create `internal/pattern/json_sample.go`:

```go
package pattern

// jsonSample memoizes parsed JSON objects for one sample slice so the
// structured probes parse each line at most once per Discover call.
// encoding/json into map[string]any is allocation-heavy; the probes
// previously re-parsed the same lines 6-9 times per call
// (jsonObjectFraction, keyFreq, observeJSONShape, plus per-probe Likely).
type jsonSample struct {
	lines  []string
	objs   []map[string]any
	parsed []bool
}

func newJSONSample(lines []string) *jsonSample {
	return &jsonSample{
		lines:  lines,
		objs:   make([]map[string]any, len(lines)),
		parsed: make([]bool, len(lines)),
	}
}

// Lines returns the sample lines (for probes that only need raw text).
func (js *jsonSample) Lines() []string { return js.lines }

// Count returns the number of sample lines.
func (js *jsonSample) Count() int { return len(js.lines) }

// Obj returns the parsed JSON object for sample line i, parsing on first
// access (ok=false when the line is not a JSON object). Not safe for
// concurrent use: probes run sequentially inside tryStructured.
func (js *jsonSample) Obj(i int) (map[string]any, bool) {
	if !js.parsed[i] {
		js.objs[i], js.parsed[i] = parseJSONLine(js.lines[i])
	}
	return js.objs[i], js.parsed[i]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestJSONSampleMemoizes ./internal/pattern/ -v`
Expected: PASS.

- [ ] **Step 5: Migrate `identify.go` to the parse-once cache**

Read `internal/pattern/identify.go` IN FULL first (866 lines). Then apply this exact mechanical rule to every probe — the change is uniform:

- `structuredProbe` type becomes:

```go
type structuredProbe struct {
	Name   string
	Likely func(js *jsonSample) bool
	Render func(js *jsonSample) (grok string, source string, ok bool)
}
```

- Every `func(sample []string) bool` closure becomes `func(js *jsonSample) bool`; every `for _, line := range sample` loop becomes an indexed loop `for i, line := range js.Lines()`; every `parseJSONLine(line)` inside such a loop becomes `js.Obj(i)` (drop the `ok` shadow — same two-value return).
- Helper functions change signature from `...(sample []string)` to `...(js *jsonSample)` and follow the same indexed-loop rule: `jsonObjectFraction(js *jsonSample) float64`, `keyFreq(js *jsonSample) (int, map[string]int)`, `observeJSONShape(js *jsonSample) jsonShape` (its `parseJSONLine(line)` call becomes `js.Obj(i)`; `shape.FirstKeys` keeps using the raw line via `js.Lines()[i]`).

Worked example — `jsonObjectFraction` before:

```go
func jsonObjectFraction(sample []string) float64 {
	if len(sample) == 0 {
		return 0
	}
	hits := 0
	for _, line := range sample {
		if _, ok := parseJSONLine(line); ok {
			hits++
		}
	}
	return float64(hits) / float64(len(sample))
}
```

after:

```go
func jsonObjectFraction(js *jsonSample) float64 {
	if js.Count() == 0 {
		return 0
	}
	hits := 0
	for i := range js.Lines() {
		if _, ok := js.Obj(i); ok {
			hits++
		}
	}
	return float64(hits) / float64(js.Count())
}
```

Apply the same transformation to all 21 probes (`dockerJSONProbe` … `tsvProbe`, listed at `identify.go:17-39`) and every helper that calls `parseJSONLine`. Non-JSON probes (logfmt, CEF, LEEF, W3C, CSV, TSV, logcat, etc.) only need the `js.Lines()` change — no parse memoization applies to them.

- [ ] **Step 6: Build `jsonSample` once in `tryStructured`**

In `internal/pattern/discovery.go` `tryStructured`, replace the loop header:

```go
func tryStructured(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern {
	js := newJSONSample(sample)
	var best *DiscoveredPattern
	for _, probe := range structuredProbes {
		if !probe.Likely(js) {
			continue
		}
		grok, source, ok := probe.Render(js)
```

(keeps the Task 5 `ctl` parameter — that change is orthogonal.)

- [ ] **Step 7: Run everything**

Run: `gofmt -l internal/ pkg/ ; go vet ./... && go test -race ./internal/pattern/ ./pkg/... && go test ./test/benchmark/ -run TestDiscoverCorrectnessSuite`
Expected: no gofmt output, all PASS — the correctness suite is byte-for-byte on all 100+ cases.

- [ ] **Step 8: Commit**

```bash
git add internal/pattern/json_sample.go internal/pattern/json_sample_test.go internal/pattern/identify.go internal/pattern/discovery.go
git commit -m "perf: parse JSON sample lines once across all structured probes"
```

---

### Task 11: Final verification and benchmark comparison

**Files:**
- Modify: none (evidence task)

**Interfaces:**
- Consumes: all prior tasks
- Produces: recorded before/after numbers

- [ ] **Step 1: Full correctness gate**

```bash
go vet ./... && gofmt -l internal/ pkg/ cmd/
go test -race ./internal/pattern/ ./pkg/...
go test ./test/benchmark/ -run TestDiscoverCorrectnessSuite -v
```

Expected: vet clean, no gofmt output, all race tests pass, all 100+ cases byte-identical.

- [ ] **Step 2: GOMAXPROCS matrix on the correctness suite**

```bash
GOMAXPROCS=1 go test ./internal/pattern/ ./pkg/... ./test/benchmark/ -run Test
GOMAXPROCS=2 go test ./internal/pattern/ -run Test
```

Expected: PASS both — single-core takes the sequential path, results identical.

- [ ] **Step 3: After benchmarks**

```bash
go test -bench=. -benchmem -run='^$' ./test/benchmark/ | tee /tmp/opencode/bench-after.txt
```

Compare against Task 0's `/tmp/opencode/bench-before.txt`. Acceptance:
- `BenchmarkDiscoverScale/lines=1000000`: **≥2× faster** than the 286 ms baseline (target <100 ms on an 18-core machine; ≥2× is the gate for smaller machines).
- `BenchmarkDiscoverByCase/*` (small inputs): within **±10%** of baseline (they stay sequential; parallel overhead must not leak in).
- `BenchmarkDiscoverMultiScale/lines=100000`: ≥2× faster than 374 ms.
- No benchmark result changed in KIND (same Grok outputs — the correctness suite already gates this).

- [ ] **Step 4: Record results**

Paste the before/after `BenchmarkDiscoverScale` and `BenchmarkDiscoverMultiScale` lines into the PR description, plus the alloc delta for `TestScoreLibraryOnSample` paths if visible in benchmem.

- [ ] **Step 5: No commit** (evidence only), or commit benchmark notes to the PR description if the team convention stores them.

---

## Deferred (out of scope for this plan — evaluate separately)

- **Required-literal prefilter for library patterns** (skip `MatchString` when a mandatory literal substring is absent from the line). Good follow-up after Task 6; needs a safe literal extractor from compiled regexes.
- **Process-wide `CompileGrok` cache** keyed by pattern string, invalidated by `patternStateVersion` (extend the `tileExprRe` pattern). Moderate win on small inputs; touches many call sites.
- **Streaming/reservoir input in the CLI** (`readLines` reads whole files into memory; documented behavior, changes `Truncated` semantics — needs a spec decision).
- **Parallelizing `tiledUnion`'s per-line `remaining` loop** (maintains cross-candidate state; small win).
- **Third-party regex engine** (DFA-based) — violates the zero-dependency constraint; revisit only if Tasks 1–6 leave regexp on top.
