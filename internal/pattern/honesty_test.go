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

func TestDiscoverUsefulShortDrainSingleClusterStillWins(t *testing.T) {
	lines := []string{
		`worker alpha processed 17 jobs from queue fast`,
		`worker beta processed 22 jobs from queue slow`,
		`worker gamma processed 19 jobs from queue default`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "drain" {
		t.Fatalf("source = %q, want drain", dp.Source)
	}
	if !strings.Contains(dp.Grok, `%{WORD:worker}`) || !strings.Contains(dp.Grok, `%{INT:processed}`) {
		t.Fatalf("grok = %q, want typed Drain captures", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverUsefulShortDrainMultiStillWins(t *testing.T) {
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
	if !strings.HasPrefix(dp.Source, "drain:multi(") {
		t.Fatalf("source = %q, want supported drain:multi union", dp.Source)
	}
	if strings.Contains(dp.Grok, "id=100") || strings.Contains(dp.Grok, "key=user:1") {
		t.Fatalf("grok is too literal: %q", dp.Grok)
	}
	if !strings.Contains(dp.Grok, `%{NOTSPACE:value}`) || !strings.Contains(dp.Grok, `%{WORD:status}`) {
		t.Fatalf("grok = %q, want generalized typed branches", dp.Grok)
	}
	assertFullCoverage(t, dp, lines)
}

func TestDiscoverRepeatedLiteralClustersFallBack(t *testing.T) {
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
	if dp.SourceFamily == "drain" {
		t.Fatalf("source = %q, want fallback for repeated literal clusters", dp.Source)
	}
	if dp.Grok != `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want log-level fallback", dp.Grok)
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
	if dp.SourceFamily == "drain" {
		t.Fatalf("source = %q, want fallback for weak minority Drain cluster", dp.Source)
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
