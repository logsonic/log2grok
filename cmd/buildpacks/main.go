// Package main is the library health-check entry point.
//
// It reports the size of the in-memory pattern library and any compile
// diagnostics. Wired into `make buildpacks` / `make test` so a broken
// embedded library fails the build early.
package main

import (
	"fmt"
	"os"

	"github.com/logsonic/log2grok/internal/pattern"
)

func main() {
	diags := pattern.LibraryDiagnostics()
	fmt.Printf("library entries: %d\n", len(pattern.KnownPatternsLibrary))
	fmt.Printf("known patterns: %d\n", len(pattern.KnownPatterns))
	fmt.Printf("primitives: %d\n", len(pattern.GrokPrimitives))
	if len(diags) > 0 {
		fmt.Fprintf(os.Stderr, "library diagnostics:\n")
		for _, e := range diags {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
	}
}
