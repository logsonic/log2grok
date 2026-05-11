package log2grok

import (
	"errors"
	"testing"
)

func TestDiscoverExposesCustomPatternsForRoundTrip(t *testing.T) {
	lines := []string{
		`[Wed Apr 29 12:00:00.123456 2026] [core:error] [pid 1234] [client 192.0.2.10:443] request failed`,
		`[Wed Apr 29 12:00:01.123456 2026] [core:error] [pid 1235] [client 192.0.2.11:443] request failed again`,
	}

	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.CustomPatterns["APACHE_ERROR_TIME"] == "" {
		t.Fatalf("APACHE_ERROR_TIME custom pattern was not exposed")
	}
	re, err := CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		t.Fatalf("CompileGrok returned error: %v", err)
	}
	if matched := EvaluateCoverage(re, lines); matched != len(lines) {
		t.Fatalf("coverage = %d/%d, want full coverage", matched, len(lines))
	}
}

func TestDiscoverTopK_ReturnsRankedCandidates(t *testing.T) {
	lines := []string{
		`192.168.1.1 - - [23/Jan/2026:14:05:01 +0000] "GET / HTTP/1.1" 200 1`,
		`10.0.0.1 - - [23/Jan/2026:14:05:02 +0000] "GET /a HTTP/1.1" 200 2`,
		`10.0.0.2 - - [23/Jan/2026:14:05:03 +0000] "GET /b HTTP/1.1" 200 3`,
	}
	results, err := DiscoverTopK(lines, 3, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("DiscoverTopK: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if len(results) > 3 {
		t.Errorf("expected ≤3 candidates, got %d", len(results))
	}
	// MatchedCount should be non-increasing.
	for i := 1; i < len(results); i++ {
		if results[i].MatchedCount > results[i-1].MatchedCount {
			t.Errorf("candidates not ranked by MatchedCount: pos %d=%d > pos %d=%d",
				i, results[i].MatchedCount, i-1, results[i-1].MatchedCount)
		}
	}
	// First candidate should populate TimestampHint (HTTPDATE present).
	if results[0].TimestampHint.IsZero() {
		t.Errorf("top candidate should have a TimestampHint, got %+v", results[0].TimestampHint)
	}
}

func TestDiscoverTopK_ErrEmptyInput(t *testing.T) {
	if _, err := DiscoverTopK(nil, 3, Options{}); err == nil {
		t.Error("expected error on nil input")
	}
	if _, err := DiscoverTopK([]string{"", ""}, 3, Options{}); !errors.Is(err, ErrEmptyInput) {
		t.Errorf("want ErrEmptyInput, got %v", err)
	}
}

// TestEndToEnd_DiscoverThenDecodeWithHint exercises the full flow that
// matters most to library consumers: Discover a pattern, build a
// Decoder from it, decode lines, and pull a time.Time out via the
// TimestampHint — no manual sniffing on the caller's side.
func TestEndToEnd_DiscoverThenDecodeWithHint(t *testing.T) {
	lines := []string{
		`192.168.1.1 - - [23/Jan/2026:14:05:01 +0000] "GET / HTTP/1.1" 200 1 "-" "ua"`,
		`10.0.0.1 - - [23/Jan/2026:14:05:02 +0000] "GET /a HTTP/1.1" 200 2 "-" "ua"`,
		`10.0.0.2 - - [23/Jan/2026:14:05:03 +0000] "GET /b HTTP/1.1" 200 3 "-" "ua"`,
	}
	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if dp.TimestampHint.IsZero() {
		t.Fatalf("TimestampHint empty for %s; pattern=%s", dp.Source, dp.Grok)
	}
	dec, err := NewDecoder(PatternSpec{
		Grok:           dp.Grok,
		CustomPatterns: dp.CustomPatterns,
	}, DecoderOptions{SmartDecode: true})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	results := dec.Decode(lines)
	for i, r := range results {
		if !r.Matched {
			t.Fatalf("line %d unmatched: %s", i, r.Error)
		}
		ts, err := dec.Timestamp(r)
		if err != nil {
			t.Fatalf("Timestamp[%d]: %v (hint=%+v fields=%v)", i, err, dec.TimestampHint(), r.Fields)
		}
		if ts.Year() != 2026 {
			t.Errorf("ts[%d].Year = %d", i, ts.Year())
		}
		if len(r.Entities.IPv4) == 0 {
			t.Errorf("Entities[%d].IPv4 empty (smart decode)", i)
		}
	}
}

func TestDiscoverTopK_DefaultK(t *testing.T) {
	// k=0 should default to 5.
	lines := []string{
		`2026-03-15T10:20:30Z INFO hello`,
		`2026-03-15T10:20:31Z WARN world`,
	}
	results, err := DiscoverTopK(lines, 0, Options{LibraryThreshold: 0.5})
	if err != nil {
		t.Fatalf("DiscoverTopK: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("k=0 should cap at 5, got %d", len(results))
	}
}
