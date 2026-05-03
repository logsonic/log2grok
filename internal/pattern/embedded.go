package pattern

import (
	"embed"
	"encoding/json"
	"fmt"
)

// embeddedFS holds the JSON-serialized defaults that ship in the binary.
// They are the canonical source-of-truth for the library and primitives.
// LoadConfig may replace the package-level vars with disk-loaded copies,
// but if those copies are missing or corrupt the embedded defaults are
// used as the fallback (and re-seeded to disk).
//
//go:embed embedded/primitives.json embedded/patterns.json
var embeddedFS embed.FS

// embeddedFile names the on-disk filenames a config directory uses. They
// match the //go:embed paths so that ReadFile can resolve them by basename.
const (
	embeddedDirPrefix  = "embedded/"
	fileNamePrimitives = "primitives.json"
	fileNamePatterns   = "patterns.json"
)

// allEmbeddedFiles is the canonical ordered list of files we expose to disk.
// Order is stable so seed/reset operations are deterministic.
var allEmbeddedFiles = []string{
	fileNamePrimitives,
	fileNamePatterns,
}

// readEmbedded returns the raw bytes for an embedded resource by basename.
func readEmbedded(name string) ([]byte, error) {
	return embeddedFS.ReadFile(embeddedDirPrefix + name)
}

func init() {
	if err := loadEmbeddedDefaults(); err != nil {
		panic(fmt.Errorf("pattern: failed to load embedded defaults: %w", err))
	}
	RefreshLibrary()
}

// loadEmbeddedDefaults populates the package-level vars (GrokPrimitives,
// KnownPatternsLibrary) from the embedded JSON. Called from init() and
// by LoadConfig as a fallback.
func loadEmbeddedDefaults() error {
	prim, err := decodePrimitives(mustReadEmbedded(fileNamePrimitives))
	if err != nil {
		return fmt.Errorf("primitives: %w", err)
	}
	library, err := decodePatterns(mustReadEmbedded(fileNamePatterns))
	if err != nil {
		return fmt.Errorf("patterns: %w", err)
	}
	FillEmptyDescriptionsInPlace(library)

	GrokPrimitives = prim
	GrokPrimitivesOverrides = GrokPrimitives
	KnownPatternsLibrary = library
	return nil
}

func mustReadEmbedded(name string) []byte {
	b, err := readEmbedded(name)
	if err != nil {
		panic(fmt.Errorf("pattern: missing embedded resource %q: %w", name, err))
	}
	return b
}

func decodePrimitives(data []byte) (map[string]string, error) {
	out := map[string]string{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodePatterns(data []byte) ([]KnownPattern, error) {
	var out []KnownPattern
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncodePrimitives serializes a primitives map as the on-disk JSON shape
// (a single JSON object), pretty-printed with two-space indent so the
// file diffs cleanly when committed to source control.
func EncodePrimitives(in map[string]string) ([]byte, error) {
	return json.MarshalIndent(in, "", "  ")
}

// EncodePatterns serializes a slice of KnownPattern as the on-disk JSON
// shape (a JSON array), pretty-printed with two-space indent.
func EncodePatterns(in []KnownPattern) ([]byte, error) {
	return json.MarshalIndent(in, "", "  ")
}

// FileNamePrimitives and FileNamePatterns expose the canonical filenames
// used inside the externalized config directory.
const (
	FileNamePrimitives = fileNamePrimitives
	FileNamePatterns   = fileNamePatterns
)

// RefreshLibrary recomputes derived structures (KnownPatterns) from the
// current values of the source vars (GrokPrimitives,
// KnownPatternsLibrary). It also resets the compiled-library cache so
// subsequent matching uses the new patterns.
//
// Call this after directly mutating any of the source vars (or after
// LoadConfig).
func RefreshLibrary() {
	composeKnownPatterns()
	resetCompiledLibrary()
}
