// Package log2grok is the public, importable API for the log2grok project.
// It delegates to internal/pattern so external suites can call Discover,
// CompileGrok, EvaluateCoverage, and LibraryDiagnostics without reaching
// into internal packages (which Go forbids).
package log2grok

import (
	"errors"
	"io"
	"regexp"

	"log2grok/internal/pattern"
)

// DiscoveredPattern is what Discover returns. ONE per call. Not a list.
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
	dp, err := pattern.Discover(lines, internalOpts)
	if err != nil {
		if errors.Is(err, pattern.ErrEmptyInput) {
			return nil, ErrEmptyInput
		}
		return nil, err
	}
	return &DiscoveredPattern{
		Source:         dp.Source,
		SourceFamily:   dp.SourceFamily,
		Grok:           dp.Grok,
		Coverage:       dp.Coverage,
		MatchedCount:   dp.MatchedCount,
		TotalLines:     dp.TotalLines,
		SampleLine:     dp.SampleLine,
		Truncated:      dp.Truncated,
		CustomPatterns: dp.CustomPatterns,
	}, nil
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
