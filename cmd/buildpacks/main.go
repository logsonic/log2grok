// Package main is the buildpacks generator entry point.
//
// In this implementation, bundled pattern packs are ingested at runtime in
// internal/pattern/bundle.go via init(); there is no separate generation
// step. This stub exists so `make buildpacks` runs successfully and so that
// future hand-off to a code generator has a place to live.
package main

import (
	"fmt"
	"os"

	"log2grok/internal/pattern"
)

func main() {
	diags := pattern.LibraryDiagnostics()
	fmt.Printf("bundled packs: %d\n", len(pattern.BuiltinPatternPacks))
	fmt.Printf("known patterns: %d\n", len(pattern.KnownPatterns))
	fmt.Printf("primitives (override): %d\n", len(pattern.GrokPrimitives))
	fmt.Printf("primitives (bundled): %d\n", len(pattern.GrokPrimitivesBundled))
	if len(diags) > 0 {
		fmt.Fprintf(os.Stderr, "library diagnostics:\n")
		for _, e := range diags {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
	}
}
