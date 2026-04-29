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
