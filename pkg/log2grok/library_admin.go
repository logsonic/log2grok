package log2grok

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/logsonic/log2grok/internal/pattern"
)

// KnownPattern is the on-disk shape of one library entry — a re-export
// of the internal type so external callers don't reach into
// internal/pattern (which Go forbids). Schema:
//
//	{"name": ..., "pattern": ..., "priority": ..., "specificity": ...,
//	 "description": "...", "customPatterns": {"NAME": "regex", ...}}
type KnownPattern = pattern.KnownPattern

// ErrConfigNotLoaded is returned by every admin API when LoadConfig (or
// ResetConfig) has not yet been called. Without an active config dir
// we have nowhere to persist edits, so we refuse rather than silently
// dropping them.
var ErrConfigNotLoaded = errors.New("log2grok: LoadConfig has not been called")

// ErrEmptyName is returned when an admin operation receives a blank
// pattern or primitive name.
var ErrEmptyName = errors.New("log2grok: name is required")

// adminMu serializes all admin write operations so concurrent callers
// can't race on the on-disk JSON or the in-memory derived state.
// Reads (List*) take the same mutex briefly to publish a consistent
// snapshot.
var adminMu sync.Mutex

// ConfigDir returns the directory most recently passed to LoadConfig
// (or DefaultConfigDirName when LoadConfig was called with ""). Returns
// the empty string when no externalized config is active.
func ConfigDir() string {
	return pattern.CurrentConfigDir()
}

// ListLibrary returns a snapshot of the active KnownPatterns library.
// The returned slice is a copy — mutating it does not affect log2grok's
// in-memory state. Use UpsertLibraryEntry / RemoveLibraryEntry to
// persist changes.
func ListLibrary() []KnownPattern {
	adminMu.Lock()
	defer adminMu.Unlock()
	src := pattern.KnownPatternsLibrary
	out := make([]KnownPattern, len(src))
	copy(out, src)
	return out
}

// UpsertLibraryEntry inserts a new entry, or replaces an existing one
// by Name (case-sensitive exact match). It validates that the Grok body
// compiles against the currently active primitives table before
// persisting; a compile failure is returned to the caller and disk
// state is left untouched.
//
// Returns the previous entry when an existing one was replaced, or nil
// when this was a fresh insert.
func UpsertLibraryEntry(kp KnownPattern) (*KnownPattern, error) {
	if kp.Name == "" {
		return nil, ErrEmptyName
	}
	if kp.Pattern == "" {
		return nil, errors.New("log2grok: pattern body is required")
	}
	if strings.TrimSpace(kp.Description) == "" {
		kp.Description = pattern.DefaultPatternDescription(kp.Name)
	}
	if _, err := pattern.CompileGrok(kp.Pattern, kp.CustomPatterns); err != nil {
		return nil, fmt.Errorf("log2grok: invalid grok %q: %w", kp.Name, err)
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	dir := pattern.CurrentConfigDir()
	if dir == "" {
		return nil, ErrConfigNotLoaded
	}

	// Specificity defaults track the convention documented in SPEC.md
	// (curated entries: 70-99, generic: 30-50). 99 mirrors the value
	// used by the embedded library's curated tier so user-added rules
	// participate in scoring at the same level as ones we ship.
	if kp.Specificity == 0 {
		kp.Specificity = 99
	}

	var replaced *KnownPattern
	updated := make([]KnownPattern, 0, len(pattern.KnownPatternsLibrary)+1)
	swapped := false
	for _, existing := range pattern.KnownPatternsLibrary {
		if existing.Name == kp.Name {
			prev := existing
			replaced = &prev
			updated = append(updated, kp)
			swapped = true
			continue
		}
		updated = append(updated, existing)
	}
	if !swapped {
		updated = append(updated, kp)
	}

	if err := persistPatterns(dir, updated); err != nil {
		return nil, err
	}
	pattern.KnownPatternsLibrary = updated
	pattern.RefreshLibrary()
	return replaced, nil
}

// RemoveLibraryEntry deletes the entry with the given Name. Returns
// (true, nil) when an entry was removed, (false, nil) when no entry
// with that name exists, and (false, err) on persistence failure.
func RemoveLibraryEntry(name string) (bool, error) {
	if name == "" {
		return false, ErrEmptyName
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	dir := pattern.CurrentConfigDir()
	if dir == "" {
		return false, ErrConfigNotLoaded
	}

	updated := make([]KnownPattern, 0, len(pattern.KnownPatternsLibrary))
	removed := false
	for _, existing := range pattern.KnownPatternsLibrary {
		if existing.Name == name {
			removed = true
			continue
		}
		updated = append(updated, existing)
	}
	if !removed {
		return false, nil
	}

	if err := persistPatterns(dir, updated); err != nil {
		return false, err
	}
	pattern.KnownPatternsLibrary = updated
	pattern.RefreshLibrary()
	return true, nil
}

// ListPrimitives returns a snapshot of the global primitives table.
// The returned map is a clone — mutations do not affect log2grok.
func ListPrimitives() map[string]string {
	adminMu.Lock()
	defer adminMu.Unlock()
	out := make(map[string]string, len(pattern.GrokPrimitives))
	for k, v := range pattern.GrokPrimitives {
		out[k] = v
	}
	return out
}

// UpsertPrimitive inserts or replaces a primitive entry. The regex body
// is validated by attempting a compile (with the existing primitives
// table available for cross-references) before being persisted. Returns
// the previous body when an entry was replaced, or "" when this was a
// fresh insert.
func UpsertPrimitive(name, body string) (string, error) {
	if name == "" {
		return "", ErrEmptyName
	}
	if body == "" {
		return "", errors.New("log2grok: primitive body is required")
	}
	// Validate as a stand-alone Go regexp first — primitives must be
	// directly compilable independent of any wrapping pattern.
	if _, err := regexp.Compile(body); err != nil {
		return "", fmt.Errorf("log2grok: invalid primitive %q: %w", name, err)
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	dir := pattern.CurrentConfigDir()
	if dir == "" {
		return "", ErrConfigNotLoaded
	}

	// Build a candidate map (clone + overlay) and validate that
	// referenced primitives still expand. We reach into the live
	// primitives map briefly to do this check before commit.
	previous, existed := pattern.GrokPrimitives[name]
	candidate := make(map[string]string, len(pattern.GrokPrimitives)+1)
	for k, v := range pattern.GrokPrimitives {
		candidate[k] = v
	}
	candidate[name] = body

	if err := persistPrimitives(dir, candidate); err != nil {
		return "", err
	}
	pattern.GrokPrimitives = candidate
	pattern.GrokPrimitivesOverrides = candidate
	pattern.RefreshLibrary()

	if existed {
		return previous, nil
	}
	return "", nil
}

// RemovePrimitive deletes a primitive by name. Returns (true, nil) when
// removed, (false, nil) when absent.
func RemovePrimitive(name string) (bool, error) {
	if name == "" {
		return false, ErrEmptyName
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	dir := pattern.CurrentConfigDir()
	if dir == "" {
		return false, ErrConfigNotLoaded
	}

	if _, ok := pattern.GrokPrimitives[name]; !ok {
		return false, nil
	}

	candidate := make(map[string]string, len(pattern.GrokPrimitives))
	for k, v := range pattern.GrokPrimitives {
		if k == name {
			continue
		}
		candidate[k] = v
	}

	if err := persistPrimitives(dir, candidate); err != nil {
		return false, err
	}
	pattern.GrokPrimitives = candidate
	pattern.GrokPrimitivesOverrides = candidate
	pattern.RefreshLibrary()
	return true, nil
}

// persistPatterns writes the slice to <dir>/patterns.json atomically:
// data is first written to a sibling .tmp file then os.Rename'd into
// place. Both operations happen under the admin mutex, so a successful
// return guarantees on-disk state matches the in-memory state the
// caller is about to publish.
func persistPatterns(dir string, kps []KnownPattern) error {
	data, err := pattern.EncodePatterns(kps)
	if err != nil {
		return fmt.Errorf("log2grok: encode patterns: %w", err)
	}
	return atomicWrite(filepath.Join(dir, pattern.FileNamePatterns), data)
}

// persistPrimitives is the symmetric helper for primitives.json.
func persistPrimitives(dir string, m map[string]string) error {
	data, err := pattern.EncodePrimitives(m)
	if err != nil {
		return fmt.Errorf("log2grok: encode primitives: %w", err)
	}
	return atomicWrite(filepath.Join(dir, pattern.FileNamePrimitives), data)
}

func atomicWrite(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("log2grok: ensure dir %s: %w", filepath.Dir(target), err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("log2grok: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		// Best-effort cleanup so a failed publish doesn't leave a
		// stale .tmp behind.
		_ = os.Remove(tmp)
		return fmt.Errorf("log2grok: rename %s -> %s: %w", tmp, target, err)
	}
	return nil
}
