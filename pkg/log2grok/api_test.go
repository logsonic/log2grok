package log2grok

import "testing"

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
