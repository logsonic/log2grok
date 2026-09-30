package pattern

import "testing"

// scoreLibraryOnSample must return, for every library pattern and in the same
// input order, exactly the coverage a direct sequential EvaluateCoverage
// yields — whether the outer loop runs sequentially or in parallel. This is
// the regression guard for parallelizing the scorer: it pins both the count
// and the index→pattern mapping, so a lost write or reordered result fails.
func TestScoreLibraryOnSampleParallelEquivalence(t *testing.T) {
	sample := deterministicLines(300)

	got := scoreLibraryOnSample(sample)

	compiled := compiledKnownPatterns()
	if len(got) != len(compiled) {
		t.Fatalf("result length = %d, want %d compiled patterns", len(got), len(compiled))
	}
	for i, cp := range compiled {
		matched := EvaluateCoverage(cp.Regex, sample)
		if got[i].Pattern.Name != cp.Pattern.Name || got[i].Compiled != cp.Regex {
			t.Fatalf("result %d maps to %q, want %q (input order must be preserved)",
				i, got[i].Pattern.Name, cp.Pattern.Name)
		}
		if got[i].Matched != matched {
			t.Fatalf("pattern %q: Matched = %d, want %d", cp.Pattern.Name, got[i].Matched, matched)
		}
		if want := ratio(matched, len(sample)); got[i].SampleCoverage != want {
			t.Fatalf("pattern %q: SampleCoverage = %v, want %v", cp.Pattern.Name, got[i].SampleCoverage, want)
		}
	}
}
