package pattern

import (
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type compiledPattern struct {
	Pattern KnownPattern
	Regex   *regexp.Regexp
}

var (
	compileMu       sync.Mutex
	compiledVersion uint64
	compiledLib     []compiledPattern
	libraryDiagErrs []error
)

// resetCompiledLibrary clears the compiled-library cache so the next call
// to compiledKnownPatterns rebuilds against the current KnownPatterns.
// Called by RefreshLibrary after LoadConfig replaces the library contents.
func resetCompiledLibrary() {
	compileMu.Lock()
	defer compileMu.Unlock()
	compiledVersion = 0
	compiledLib = nil
	libraryDiagErrs = nil
}

// compiledKnownPatterns returns library entries that compiled cleanly.
// Compile errors are recorded in libraryDiagErrs.
func compiledKnownPatterns() []compiledPattern {
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
		// existing benign race window — a bump between the version read and
		// the rebuild check discards the snapshot, same as before.
		patterns := knownPatternsSnapshot()

		compiled := make([]compiledPattern, 0, len(patterns))
		var errs []error
		for _, kp := range patterns {
			re, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
			if err != nil {
				errs = append(errs, fmt.Errorf("library %q: %w", kp.Name, err))
				continue
			}
			compiled = append(compiled, compiledPattern{Pattern: kp, Regex: re})
		}

		// If config changed while we were compiling, discard this snapshot and
		// rebuild from the new version rather than publishing stale regexes.
		if currentPatternStateVersion() != version {
			continue
		}

		compileMu.Lock()
		if compiledLib == nil || compiledVersion != version {
			compiledVersion = version
			compiledLib = compiled
			libraryDiagErrs = errs
		}
		out := append([]compiledPattern(nil), compiledLib...)
		compileMu.Unlock()
		return out
	}
}

// LibraryDiagnostics returns library-compile errors (cumulative).
func LibraryDiagnostics() []error {
	compiledKnownPatterns()
	compileMu.Lock()
	defer compileMu.Unlock()
	out := make([]error, 0, len(libraryDiagErrs))
	out = append(out, libraryDiagErrs...)
	return out
}

type candidateResult struct {
	Pattern        KnownPattern
	Compiled       *regexp.Regexp
	SampleCoverage float64
	Matched        int
	FullTotal      int
}

// scoreLibraryOnSample scores every compiled library pattern against the
// sample. The outer loop parallelizes across patterns (bounded by
// GOMAXPROCS); each per-pattern scan stays sequential so parallelism is
// not nested 90×GOMAXPROCS deep. Results keep input order (indexed writes),
// so callers' subsequent stable sorts see exactly the sequential ordering.
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

// scoreOne scores one compiled pattern against the sample. The caller
// parallelizes across patterns, so this stays sequential (scanMatchesSeq):
// nesting EvaluateCoverage's own parallel scan inside would oversubscribe
// (the sample is far above parallelScanMinLines, so EvaluateCoverage would fan
// out GOMAXPROCS workers per pattern).
func scoreOne(cp compiledPattern, sample []string) candidateResult {
	matched := scanMatchesSeq(cp.Regex, sample, nil, nil)
	return candidateResult{
		Pattern:        cp.Pattern,
		Compiled:       cp.Regex,
		SampleCoverage: ratio(matched, len(sample)),
		Matched:        matched,
	}
}

// betterCandidate compares two library-stage candidates. Order:
//  1. higher match count
//  2. more typed captures (objective measure of how much of the line is
//     parsed; aligns with pickBetter's cross-stage ranking)
//  3. higher specificity (editorial nudge for hand-written entries)
//  4. fewer GREEDYDATA references
//  5. lower declaration priority
//
// Specificity sits below typed-capture count so that an entry which only
// captures `timestamp + GREEDYDATA` does not beat a structurally richer
// peer purely on hand-set specificity numbers.
func betterCandidate(next, best *candidateResult) bool {
	if best == nil {
		return true
	}
	if next.Matched != best.Matched {
		return next.Matched > best.Matched
	}
	nextTyped := typedCaptureCount(next.Pattern.Pattern)
	bestTyped := typedCaptureCount(best.Pattern.Pattern)
	if nextTyped != bestTyped {
		return nextTyped > bestTyped
	}
	if next.Pattern.Specificity != best.Pattern.Specificity {
		return next.Pattern.Specificity > best.Pattern.Specificity
	}
	nextGreedy := strings.Count(next.Pattern.Pattern, "%{GREEDYDATA")
	bestGreedy := strings.Count(best.Pattern.Pattern, "%{GREEDYDATA")
	if nextGreedy != bestGreedy {
		return nextGreedy < bestGreedy
	}
	return next.Pattern.Priority < best.Pattern.Priority
}

func keepTopCandidates(in []candidateResult, n int) []candidateResult {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Matched != in[j].Matched {
			return in[i].Matched > in[j].Matched
		}
		if in[i].Pattern.Specificity != in[j].Pattern.Specificity {
			return in[i].Pattern.Specificity > in[j].Pattern.Specificity
		}
		return in[i].Pattern.Priority < in[j].Pattern.Priority
	})
	if len(in) > n {
		return in[:n]
	}
	return in
}

func chooseSample(lines []string, max int) []string {
	if max <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= max {
		return append([]string(nil), lines...)
	}
	first := 1024
	if first > max {
		first = max
	}
	if first > len(lines) {
		first = len(lines)
	}

	out := make([]string, 0, max)
	out = append(out, lines[:first]...)

	remaining := max - len(out)
	if remaining <= 0 {
		return out
	}
	rest := len(lines) - first
	if rest <= 0 {
		return out
	}
	step := float64(rest) / float64(remaining)
	for i := 0; i < remaining; i++ {
		idx := first + int(float64(i)*step)
		if idx >= len(lines) {
			idx = len(lines) - 1
		}
		out = append(out, lines[idx])
	}
	return out
}
