package pattern

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type compiledPattern struct {
	Pattern KnownPattern
	Regex   *regexp.Regexp
}

var (
	compileMu       sync.Mutex
	compileOnce     sync.Once
	compiledLib     []compiledPattern
	libraryDiagErrs []error
)

// resetCompiledLibrary clears the compiled-library cache so the next call
// to compiledKnownPatterns rebuilds against the current KnownPatterns.
// Called by RefreshLibrary after LoadConfig replaces the library contents.
func resetCompiledLibrary() {
	compileMu.Lock()
	defer compileMu.Unlock()
	compileOnce = sync.Once{}
	compiledLib = nil
	libraryDiagErrs = nil
}

// compiledKnownPatterns returns library entries that compiled cleanly.
// Compile errors are recorded in libraryDiagErrs.
func compiledKnownPatterns() []compiledPattern {
	compileOnce.Do(func() {
		compiledLib = make([]compiledPattern, 0, len(KnownPatterns))
		for _, kp := range KnownPatterns {
			re, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
			if err != nil {
				libraryDiagErrs = append(libraryDiagErrs,
					fmt.Errorf("library %q: %w", kp.Name, err))
				continue
			}
			compiledLib = append(compiledLib, compiledPattern{Pattern: kp, Regex: re})
		}
	})
	return compiledLib
}

// LibraryDiagnostics returns library-compile errors (cumulative).
func LibraryDiagnostics() []error {
	compiledKnownPatterns()
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

func scoreLibraryOnSample(sample []string) []candidateResult {
	compiled := compiledKnownPatterns()
	out := make([]candidateResult, 0, len(compiled))
	for _, cp := range compiled {
		matched := EvaluateCoverage(cp.Regex, sample)
		out = append(out, candidateResult{
			Pattern:        cp.Pattern,
			Compiled:       cp.Regex,
			SampleCoverage: ratio(matched, len(sample)),
			Matched:        matched,
		})
	}
	return out
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
