package pattern

import "sync"

var (
	patternStateMu      sync.RWMutex
	patternStateVersion uint64
)

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneKnownPatterns(in []KnownPattern) []KnownPattern {
	if in == nil {
		return nil
	}
	out := make([]KnownPattern, len(in))
	for i, kp := range in {
		out[i] = kp
		out[i].CustomPatterns = cloneStringMap(kp.CustomPatterns)
	}
	return out
}

func currentPatternStateVersion() uint64 {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return patternStateVersion
}

// knownPatternsSnapshot returns a deep copy of the active library. Unlike
// the old snapshotWithVersion, it is only called on cache-rebuild paths —
// the cache-hit path reads the version alone, without cloning the library
// and its CustomPatterns maps on every Discover call.
func knownPatternsSnapshot() []KnownPattern {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return cloneKnownPatterns(KnownPatterns)
}

// KnownPatternsLibrarySnapshot returns a deep copy of the active source
// library. Admin APIs use this instead of reading KnownPatternsLibrary
// directly so config edits can run concurrently with discovery.
func KnownPatternsLibrarySnapshot() []KnownPattern {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return cloneKnownPatterns(KnownPatternsLibrary)
}

// GrokPrimitivesSnapshot returns a copy of the active primitive table.
func GrokPrimitivesSnapshot() map[string]string {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return cloneStringMap(GrokPrimitives)
}

// ReplaceKnownPatternsLibrary commits a new source library and rebuilds all
// derived caches.
func ReplaceKnownPatternsLibrary(updated []KnownPattern) {
	patternStateMu.Lock()
	KnownPatternsLibrary = cloneKnownPatterns(updated)
	FillEmptyDescriptionsInPlace(KnownPatternsLibrary)
	composeKnownPatternsLocked()
	patternStateVersion++
	patternStateMu.Unlock()
	resetCompiledLibrary()
	resetTilerCaches()
}

// ReplaceGrokPrimitives commits a new primitive table and rebuilds all
// derived caches.
func ReplaceGrokPrimitives(updated map[string]string) {
	patternStateMu.Lock()
	GrokPrimitives = cloneStringMap(updated)
	GrokPrimitivesOverrides = GrokPrimitives
	composeKnownPatternsLocked()
	patternStateVersion++
	patternStateMu.Unlock()
	resetCompiledLibrary()
	resetTilerCaches()
}

func commitPatternState(primitives map[string]string, library []KnownPattern, configDir *string) {
	patternStateMu.Lock()
	GrokPrimitives = cloneStringMap(primitives)
	GrokPrimitivesOverrides = GrokPrimitives
	KnownPatternsLibrary = cloneKnownPatterns(library)
	FillEmptyDescriptionsInPlace(KnownPatternsLibrary)
	if configDir != nil {
		currentConfigDir = *configDir
	}
	composeKnownPatternsLocked()
	patternStateVersion++
	patternStateMu.Unlock()
	resetCompiledLibrary()
	resetTilerCaches()
}
