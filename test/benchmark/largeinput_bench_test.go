package benchmark

import (
	"fmt"
	"testing"

	"github.com/logsonic/log2grok/internal/pattern"
)

// genLarge builds n synthetic Nginx-style access-log lines. Used by the
// scale benchmarks below to exercise the bounded-sampling path.
func genLarge(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf(
			`10.0.0.%d - alice [15/Jan/2025:10:23:%02d +0000] "GET /index%d.html HTTP/1.1" 200 %d "https://ref.example/%d" "Mozilla/5.0"`,
			i%256, i%60, i%1000, 100+i%9000, i%50)
	}
	return out
}

// genMixed builds n lines alternating between nginx access lines and JSON,
// a two-format stream for the DiscoverMulti scale benchmark.
func genMixed(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			out[i] = fmt.Sprintf(
				`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`,
				i%256, i%60, i%100, 100+i%900)
		} else {
			out[i] = fmt.Sprintf(`{"ts":"2025-01-15T10:23:%02dZ","level":"info","msg":"job %d"}`, i%60, i)
		}
	}
	return out
}

// BenchmarkDiscoverMultiScale shows DiscoverMulti stays bounded on huge
// multi-format inputs, the same property as the single-pattern path.
func BenchmarkDiscoverMultiScale(b *testing.B) {
	opts := pattern.Options{LibraryThreshold: 0.85, TargetCoverage: 0.90}
	for _, n := range []int{100000, 1000000} {
		lines := genMixed(n)
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, err := pattern.DiscoverMulti(lines, opts)
				if err != nil || res == nil {
					b.Fatalf("DiscoverMulti: %v", err)
				}
			}
		})
	}
}

// BenchmarkDiscoverScale measures Discover across input sizes spanning the
// internal sampling cap. Past the cap, wall time and allocations stay
// roughly flat: that flatness is the property that lets log2grok handle
// 1M+ line files without breaking the bank.
func BenchmarkDiscoverScale(b *testing.B) {
	opts := pattern.Options{LibraryThreshold: 0.75}
	for _, n := range []int{10000, 100000, 1000000} {
		lines := genLarge(n)
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				dp, err := pattern.Discover(lines, opts)
				if err != nil || dp == nil {
					b.Fatalf("discover: %v", err)
				}
			}
		})
	}
}
