// Package log2grok is the public, importable API for the log2grok project.
// It delegates to internal/pattern so external suites can call Discover,
// CompileGrok, EvaluateCoverage, and LibraryDiagnostics without reaching
// into internal packages (which Go forbids).
package log2grok

import (
	"errors"
	"io"
	"regexp"

	"github.com/logsonic/log2grok/internal/pattern"
)

// DiscoveredPattern is what Discover returns. ONE per call. Not a list.
//
// TimestampHint is auto-derived from the chosen Grok body. It points at
// whichever capture (and Go time layout) is most likely to hold the
// line's wall-clock time. Callers can pass it to ParseTimestamp with a
// LineResult.Fields map to avoid hand-rolling a resolver. The hint is
// best-effort: zero-value when no recognized primitive is present,
// MultiField-only when a SYSLOG-style split is detected.
type DiscoveredPattern struct {
	Source         string
	SourceFamily   string
	Grok           string
	Coverage       float64
	MatchedCount   int
	TotalLines     int
	SampleLine     string
	Truncated      bool
	CustomPatterns map[string]string
	TimestampHint  TimestampHint
}

// Options controls Discover's behavior.
type Options struct {
	LibraryThreshold float64
	MaxLines         int
	Verbose          bool
	Diagnostics      io.Writer
}

// ErrEmptyInput is returned when the input has no non-empty lines.
var ErrEmptyInput = errors.New("log2grok: no non-empty input lines")

// Discover returns the single best Grok pattern for the input lines.
func Discover(lines []string, opts Options) (*DiscoveredPattern, error) {
	internalOpts := pattern.Options{
		LibraryThreshold: opts.LibraryThreshold,
		MaxLines:         opts.MaxLines,
		Verbose:          opts.Verbose,
		Diagnostics:      opts.Diagnostics,
	}
	// Add a safety check for lines length
	if len(lines) == 0 {
		return nil, errors.New("log2grok: no input lines")
	}	
	if len(lines) > 100000 {
		return nil, errors.New("log2grok: input lines too long, max is 100000")
	}
	dp, err := pattern.Discover(lines, internalOpts)
	if err != nil {
		if errors.Is(err, pattern.ErrEmptyInput) {
			return nil, ErrEmptyInput
		}
		return nil, err
	}
	out := &DiscoveredPattern{
		Source:         dp.Source,
		SourceFamily:   dp.SourceFamily,
		Grok:           dp.Grok,
		Coverage:       dp.Coverage,
		MatchedCount:   dp.MatchedCount,
		TotalLines:     dp.TotalLines,
		SampleLine:     dp.SampleLine,
		Truncated:      dp.Truncated,
		CustomPatterns: dp.CustomPatterns,
	}
	out.TimestampHint = inferTimestampHint(out.Grok)
	return out, nil
}

// DiscoverTopK returns up to k candidate patterns ranked by match
// count → typed captures → specificity. Useful for UIs that want to
// surface alternatives ("did you mean Nginx Combined or Nginx Common?")
// instead of a single committed answer. Candidates with zero matches
// are dropped. When the library stage produces fewer than k candidates,
// a structured probe is appended if it matched anything.
//
// Each DiscoveredPattern in the result has its TimestampHint populated
// the same way Discover does. K <= 0 defaults to 5.
func DiscoverTopK(lines []string, k int, opts Options) ([]*DiscoveredPattern, error) {
	internalOpts := pattern.Options{
		LibraryThreshold: opts.LibraryThreshold,
		MaxLines:         opts.MaxLines,
		Verbose:          opts.Verbose,
		Diagnostics:      opts.Diagnostics,
	}
	if len(lines) == 0 {
		return nil, errors.New("log2grok: no input lines")
	}
	if len(lines) > 100000 {
		return nil, errors.New("log2grok: input lines too long, max is 100000")
	}
	dps, err := pattern.DiscoverTopK(lines, k, internalOpts)
	if err != nil {
		if errors.Is(err, pattern.ErrEmptyInput) {
			return nil, ErrEmptyInput
		}
		return nil, err
	}
	out := make([]*DiscoveredPattern, 0, len(dps))
	for _, dp := range dps {
		entry := &DiscoveredPattern{
			Source:         dp.Source,
			SourceFamily:   dp.SourceFamily,
			Grok:           dp.Grok,
			Coverage:       dp.Coverage,
			MatchedCount:   dp.MatchedCount,
			TotalLines:     dp.TotalLines,
			SampleLine:     dp.SampleLine,
			Truncated:      dp.Truncated,
			CustomPatterns: dp.CustomPatterns,
		}
		entry.TimestampHint = inferTimestampHint(entry.Grok)
		out = append(out, entry)
	}
	return out, nil
}

// CompileGrok expands all %{NAME}, %{NAME:field}, and %{NAME:field:type}
// references and compiles the result as an anchored Go regexp.
func CompileGrok(p string, extras map[string]string) (*regexp.Regexp, error) {
	return pattern.CompileGrok(p, extras)
}

// EvaluateCoverage runs re against every line; returns count of matches.
func EvaluateCoverage(re *regexp.Regexp, lines []string) int {
	return pattern.EvaluateCoverage(re, lines)
}

// LibraryDiagnostics returns errors that occurred while compiling the
// built-in library at startup.
func LibraryDiagnostics() []error {
	return pattern.LibraryDiagnostics()
}

// DefaultConfigDirName is the directory created in the current working
// directory when LoadConfig is called without an explicit path.
const DefaultConfigDirName = pattern.DefaultConfigDirName

// LoadConfig seeds and loads the per-project pattern library from disk.
//
// On startup the embedded default JSON files are copied to dir if missing.
// Existing files are loaded and replace the in-memory defaults; if a file
// is corrupt it is moved to a timestamped backup, the embedded default is
// re-seeded, and a warning is written to warn (when non-nil).
//
// Pass an empty dir to use ./.log2grok.
func LoadConfig(dir string, warn io.Writer) error {
	return pattern.LoadConfig(dir, warn)
}

// ResetConfig forcibly overwrites every file under dir with the embedded
// default. Existing files are renamed to ".bak.<timestamp>" first.
func ResetConfig(dir string, warn io.Writer) error {
	return pattern.ResetConfig(dir, warn)
}
