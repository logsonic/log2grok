package pattern

import "testing"

func TestFillEmptyDescriptionsInPlace(t *testing.T) {
	lib := []KnownPattern{
		{Name: "Traefik Access", Pattern: "%{GREEDYDATA:msg}", Description: ""},
		{Name: "Already set", Pattern: "%{IP:a}", Description: "Custom blurb"},
	}
	FillEmptyDescriptionsInPlace(lib)
	if lib[0].Description != "Grok pattern for Traefik Access logs." {
		t.Errorf("unexpected fill: %q", lib[0].Description)
	}
	if lib[1].Description != "Custom blurb" {
		t.Errorf("should not overwrite existing description: %q", lib[1].Description)
	}
}

// After a library replacement the compiled cache must reflect the new library
// on the next call. That is the version-bump (rebuild) path — the one that
// still clones the library after the cache-hit clone is removed.
func TestCompiledCacheTracksLibraryReplacement(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

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

// The compiled-cache HIT path must not deep-clone the library: it should only
// copy the compiled-pattern slice (one allocation). Before this refactor the
// hit path cloned every KnownPattern and its CustomPatterns map on every
// Discover call. This is the actual performance contract of the change, so it
// is asserted directly rather than via a benchmark.
func TestCompiledCacheHitDoesNotCloneLibrary(t *testing.T) {
	if len(compiledKnownPatterns()) == 0 {
		t.Fatal("empty compiled library")
	}
	// Warming the cache makes the measured loop a pure cache hit even when an
	// earlier test left it stale.
	alloc := testing.AllocsPerRun(50, func() {
		_ = compiledKnownPatterns()
	})
	if alloc > 2 {
		t.Fatalf("cache-hit compiledKnownPatterns allocates %.0f times/run, want <= 2 "+
			"(library is being deep-cloned on every call)", alloc)
	}
}
