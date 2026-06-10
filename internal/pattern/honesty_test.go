package pattern

import (
	"strings"
	"testing"
)

func TestDiscoverNoTimestampHTTPFallback(t *testing.T) {
	lines := []string{
		`GET /api/users 200 12ms`,
		`POST /api/jobs 202 31ms`,
		`DELETE /api/jobs/8 404 4ms`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	want := `%{WORD:method}\s+%{URIPATHPARAM:url}\s+%{INT:status}(?:\s+%{DURATION:duration})?`
	if dp.Source != "fallback:HTTP Request Summary" {
		t.Fatalf("source = %q, want fallback:HTTP Request Summary", dp.Source)
	}
	if dp.Grok != want {
		t.Fatalf("grok = %q, want %q", dp.Grok, want)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverUsefulShortTilingStillWins(t *testing.T) {
	lines := []string{
		`worker alpha processed 17 jobs from queue fast`,
		`worker beta processed 22 jobs from queue slow`,
		`worker gamma processed 19 jobs from queue default`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "inferred:Tiled" {
		t.Fatalf("source = %q, want inferred:Tiled", dp.Source)
	}
	// The shared vocabulary (worker/processed/jobs/queue) is the evidence
	// that lets a 3-line sample win; the varying slots must be typed.
	if !strings.Contains(dp.Grok, `%{WORD:`) || !strings.Contains(dp.Grok, `%{INT:`) {
		t.Fatalf("grok = %q, want typed captures", dp.Grok)
	}
	if !strings.Contains(dp.Grok, `worker`) || !strings.Contains(dp.Grok, `processed`) {
		t.Fatalf("grok = %q, want shared keywords kept as literals", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverUsefulShortMixedShapesStillWin(t *testing.T) {
	lines := []string{
		`API request id=100 status=ok`,
		`API request id=101 status=fail`,
		`CACHE fill key=user:1 status=ok`,
		`CACHE fill key=user:2 status=miss`,
		`API request id=102 status=ok`,
		`CACHE fill key=user:3 status=ok`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	// The two shapes share a token layout, so the population-voting tiler
	// generalizes them into ONE typed pattern (the old drain engine needed
	// a two-branch union here).
	if dp.Source != "inferred:Tiled" {
		t.Fatalf("source = %q, want inferred:Tiled", dp.Source)
	}
	if strings.Contains(dp.Grok, "id=100") || strings.Contains(dp.Grok, "key=user:1") {
		t.Fatalf("grok is too literal: %q", dp.Grok)
	}
	if !strings.Contains(dp.Grok, `%{NOTSPACE:`) || !strings.Contains(dp.Grok, `%{WORD:`) {
		t.Fatalf("grok = %q, want generalized typed captures", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverRepeatedLiteralClustersGeneralize(t *testing.T) {
	lines := []string{
		`ERROR static api down`,
		`ERROR static api down`,
		`WARN static cache slow`,
		`WARN static cache slow`,
		`INFO static worker ready`,
		`INFO static worker ready`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	// All six lines share the `<LEVEL> static <w> <w>` layout, the level
	// varies, and `static` is shared vocabulary — the tiler may generalize.
	// What it must NOT do is freeze whole sample lines as literals.
	if strings.Contains(dp.Grok, "api down") || strings.Contains(dp.Grok, "cache slow") {
		t.Fatalf("grok froze sample text: %q", dp.Grok)
	}
	if !strings.Contains(dp.Grok, `%{LOGLEVEL:level}`) {
		t.Fatalf("grok = %q, want a typed level capture", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverWeakTenLineDrainClusterFallsBack(t *testing.T) {
	lines := []string{
		`worker alpha processed 17 jobs`,
		`worker beta processed 18 jobs`,
		`cache miss user 1`,
		`db locked shard 7`,
		`payment declined card`,
		`email bounce mx`,
		`deploy started api`,
		`config reload ok`,
		`quota exceeded tenant`,
		`search timeout query`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.SourceFamily == "inferred" {
		t.Fatalf("source = %q, want fallback for weak minority shape", dp.Source)
	}
	if dp.Grok != `%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want message fallback", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func assertFullCoverage(t *testing.T, dp *DiscoveredPattern, lines []string) {
	t.Helper()
	re, err := CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		t.Fatalf("CompileGrok(%q): %v", dp.Grok, err)
	}
	if matched := EvaluateCoverage(re, lines); matched != len(lines) {
		t.Fatalf("%s coverage = %d/%d, want full coverage", dp.Source, matched, len(lines))
	}
}
