package pattern

import (
	"strings"
	"testing"
)

func TestStructuredJSONExtractsCommonFields(t *testing.T) {
	lines := []string{
		`{"time":"2026-04-29T12:00:00Z","level":"info","msg":"started","trace_id":"abc123"}`,
		`{"time":"2026-04-29T12:00:01Z","level":"warn","msg":"slow","trace_id":"def456"}`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "structured:Pino JSON" {
		t.Fatalf("source = %q, want structured:Pino JSON", dp.Source)
	}
	for _, want := range []string{
		"%{QUOTEDSTRING:timestamp}",
		"%{QUOTEDSTRING:level}",
		"%{QUOTEDSTRING:message}",
		"%{QUOTEDSTRING:trace_id}",
	} {
		if !strings.Contains(dp.Grok, want) {
			t.Fatalf("grok %q does not contain %q", dp.Grok, want)
		}
	}
	re, err := CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		t.Fatalf("CompileGrok returned error: %v", err)
	}
	if matched := EvaluateCoverage(re, lines); matched != len(lines) {
		t.Fatalf("coverage = %d/%d, want full coverage", matched, len(lines))
	}
}

func TestDiscoverMaxLinesSetsTruncated(t *testing.T) {
	lines := []string{
		`{"time":"2026-04-29T12:00:00Z","level":"info","msg":"one"}`,
		`{"time":"2026-04-29T12:00:01Z","level":"info","msg":"two"}`,
		`not considered`,
	}

	dp, err := Discover(lines, Options{MaxLines: 2})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if !dp.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if dp.TotalLines != 2 {
		t.Fatalf("TotalLines = %d, want 2", dp.TotalLines)
	}
}

func TestDiscoverShortHeterogeneousInputDoesNotLiteralUnion(t *testing.T) {
	lines := []string{
		`ALERT backend/api error code=E42 retry=false host=web-1`,
		`WARN cache miss key=user:123 route=/v1/users latency=14ms`,
		`INFO worker done job=778 queue=email elapsed=9ms`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if strings.HasPrefix(dp.Source, "drain:multi(") {
		t.Fatalf("source = %q, want non-drain fallback", dp.Source)
	}
	if strings.Contains(dp.Grok, "ALERT backend/api") || strings.Contains(dp.Grok, "|(?:WARN cache miss") {
		t.Fatalf("grok is a literal sample alternation: %q", dp.Grok)
	}
	if dp.Grok != `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want log-level fallback", dp.Grok)
	}
}

func TestDiscoverSingleUnknownLineDoesNotReturnLiteralDrain(t *testing.T) {
	lines := []string{`GET /api/users 200 12ms`}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.SourceFamily == "drain" {
		t.Fatalf("source = %q, want fallback for single unknown line", dp.Source)
	}
	if dp.Grok != `%{WORD:method}\s+%{URIPATHPARAM:url}\s+%{INT:status}(?:\s+%{DURATION:duration})?` {
		t.Fatalf("grok = %q, want HTTP summary fallback", dp.Grok)
	}
}

func TestDiscoverShortPartialDrainFallsBackToLogLevelMessage(t *testing.T) {
	lines := []string{
		`INFO api server started`,
		`INFO api cache warmed`,
		`INFO worker job finished`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.SourceFamily == "drain" {
		t.Fatalf("source = %q, want fallback instead of partial short-sample Drain", dp.Source)
	}
	if dp.Grok != `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want log-level fallback", dp.Grok)
	}
}
