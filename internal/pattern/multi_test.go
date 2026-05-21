package pattern

import (
	"fmt"
	"strings"
	"testing"
)

// fourFormatLog interleaves nginx, JSON, syslog, and a custom app line in
// equal quarters — a textbook multi-format stream.
func fourFormatLog(perFormat int) []string {
	var lines []string
	for i := 0; i < perFormat*4; i++ {
		switch i % 4 {
		case 0:
			lines = append(lines, fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i))
		case 1:
			lines = append(lines, fmt.Sprintf(`{"ts":"2025-01-15T10:23:%02dZ","level":"info","msg":"job %d done"}`, i%60, i))
		case 2:
			lines = append(lines, fmt.Sprintf(`Jan 15 10:23:%02d host01 sshd[%d]: Accepted password for alice from 10.0.0.%d`, i%60, 1000+i, i%256))
		case 3:
			lines = append(lines, fmt.Sprintf(`WORKER queue=email processed=%d latency=%dms`, i, i*3))
		}
	}
	return lines
}

// TestDiscoverMultiSeparatesFormats verifies that a four-format stream is
// returned as a set of distinct standalone patterns reaching the target,
// and that the union of those patterns really does match every line.
func TestDiscoverMultiSeparatesFormats(t *testing.T) {
	lines := fourFormatLog(40)
	res, err := DiscoverMulti(lines, Options{LibraryThreshold: 0.85, TargetCoverage: 0.90})
	if err != nil {
		t.Fatalf("DiscoverMulti: %v", err)
	}
	if len(res.Patterns) < 3 {
		t.Fatalf("got %d patterns, want >= 3 for a 4-format log", len(res.Patterns))
	}
	if res.CombinedCoverage < 0.90 {
		t.Fatalf("combined coverage %.3f < target 0.90", res.CombinedCoverage)
	}
	if res.TotalLines != len(lines) {
		t.Fatalf("TotalLines = %d, want %d", res.TotalLines, len(lines))
	}
	// Each suggested pattern must compile, and together they must explain
	// at least CombinedMatched lines (first-match-wins union).
	covered := make([]bool, len(lines))
	for _, p := range res.Patterns {
		re, err := CompileGrok(p.Grok, p.CustomPatterns)
		if err != nil {
			t.Fatalf("pattern %q failed to compile: %v", p.Source, err)
		}
		for i, line := range lines {
			if !covered[i] && re.MatchString(line) {
				covered[i] = true
			}
		}
	}
	got := 0
	for _, c := range covered {
		if c {
			got++
		}
	}
	if want := res.CombinedMatched; got < want {
		t.Fatalf("union matched %d lines, want >= reported %d", got, want)
	}
}

// TestDiscoverMultiStopsAtTarget confirms the selector stops once the
// combined coverage clears the target instead of enumerating every shape.
func TestDiscoverMultiStopsAtTarget(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		switch {
		case i%100 < 85: // 85% nginx
			lines = append(lines, fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i))
		default: // 15% JSON
			lines = append(lines, fmt.Sprintf(`{"ts":"2025-01-15T10:23:%02dZ","level":"info","msg":"job %d done"}`, i%60, i))
		}
	}
	res, err := DiscoverMulti(lines, Options{LibraryThreshold: 0.85, TargetCoverage: 0.90})
	if err != nil {
		t.Fatalf("DiscoverMulti: %v", err)
	}
	if res.CombinedCoverage < 0.90 {
		t.Fatalf("combined coverage %.3f < target 0.90", res.CombinedCoverage)
	}
	if len(res.Patterns) > 2 {
		t.Fatalf("got %d patterns, want <= 2 (should stop at target)", len(res.Patterns))
	}
}

// TestDiscoverMultiSingleFormat verifies that a clean single-format log is
// returned as exactly one pattern, not split.
func TestDiscoverMultiSingleFormat(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i)
	}
	res, err := DiscoverMulti(lines, Options{LibraryThreshold: 0.85})
	if err != nil {
		t.Fatalf("DiscoverMulti: %v", err)
	}
	if len(res.Patterns) != 1 {
		t.Fatalf("got %d patterns, want 1 for a single-format log", len(res.Patterns))
	}
	if !strings.Contains(res.Patterns[0].Source, "library:") {
		t.Fatalf("source = %q, want a library match", res.Patterns[0].Source)
	}
}

// TestDiscoverMultiGainFloor confirms that an irreducible long tail of
// distinct one-off lines does not produce a swarm of overfit patterns:
// the selector stops at the major formats and reports honest coverage.
func TestDiscoverMultiGainFloor(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		switch {
		case i%5 < 3: // 60% nginx
			lines = append(lines, fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i))
		case i%5 == 3: // 20% JSON
			lines = append(lines, fmt.Sprintf(`{"ts":"2025-01-15T10:23:%02dZ","level":"info","msg":"job %d"}`, i%60, i))
		default: // 20% all-distinct noise (one cluster each)
			lines = append(lines, fmt.Sprintf(`NOISE-%d-token blob%d end%d`, i, i*7, i*13))
		}
	}
	res, err := DiscoverMulti(lines, Options{LibraryThreshold: 0.85, TargetCoverage: 0.90})
	if err != nil {
		t.Fatalf("DiscoverMulti: %v", err)
	}
	if len(res.Patterns) > 3 {
		t.Fatalf("got %d patterns, want <= 3 (gain floor should suppress the noise tail)", len(res.Patterns))
	}
	if res.CombinedCoverage < 0.75 || res.CombinedCoverage > 0.86 {
		t.Fatalf("combined coverage %.3f, expected ~0.80 (the major formats only)", res.CombinedCoverage)
	}
}
