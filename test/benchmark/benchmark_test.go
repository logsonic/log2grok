package benchmark

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log2grok/internal/pattern"
)

type benchCase struct {
	Name     string
	Lines    []string
	Expected string
}

func loadCases(t testing.TB) []benchCase {
	t.Helper()

	root := filepath.Join("test", "benchmark", "cases")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read cases dir: %v", err)
	}

	out := make([]benchCase, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		name := e.Name()
		inputPath := filepath.Join(root, name, "input.log")
		expectedPath := filepath.Join(root, name, "expected.grok")

		lines := readLines(t, inputPath)
		expectedRaw, err := os.ReadFile(expectedPath)
		if err != nil {
			t.Fatalf("read %s: %v", expectedPath, err)
		}

		out = append(out, benchCase{
			Name:     name,
			Lines:    lines,
			Expected: strings.TrimSpace(string(expectedRaw)),
		})
	}

	if len(out) < 100 {
		t.Fatalf("expected at least 100 cases, got %d", len(out))
	}

	return out
}

func readLines(t testing.TB, p string) []string {
	t.Helper()

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 8*1024*1024)

	var lines []string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan %s: %v", p, err)
	}
	return lines
}

func TestDiscoverCorrectnessSuite(t *testing.T) {
	cases := loadCases(t)
	opts := pattern.Options{LibraryThreshold: 0.75}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			dp, err := pattern.Discover(tc.Lines, opts)
			if err != nil {
				t.Fatalf("discover failed: %v", err)
			}
			if dp == nil || dp.Grok == "" {
				t.Fatalf("empty discover result")
			}

			if dp.Grok != tc.Expected {
				t.Fatalf("grok mismatch\ngot:  %s\nwant: %s", dp.Grok, tc.Expected)
			}

			re, err := pattern.CompileGrok(dp.Grok, nil)
			if err != nil {
				t.Fatalf("compile returned grok failed: %v", err)
			}
			matched := pattern.EvaluateCoverage(re, tc.Lines)
			if matched != len(tc.Lines) {
				t.Fatalf("coverage %d/%d < 100%%", matched, len(tc.Lines))
			}
		})
	}
}

func BenchmarkDiscoverAllCases(b *testing.B) {
	cases := loadCases(b)
	opts := pattern.Options{LibraryThreshold: 0.75}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, tc := range cases {
			dp, err := pattern.Discover(tc.Lines, opts)
			if err != nil {
				b.Fatalf("%s: %v", tc.Name, err)
			}
			if dp == nil || dp.Grok == "" {
				b.Fatalf("%s: empty result", tc.Name)
			}
		}
	}
}

func BenchmarkDiscoverByCase(b *testing.B) {
	cases := loadCases(b)
	opts := pattern.Options{LibraryThreshold: 0.75}

	for _, tc := range cases {
		tc := tc
		b.Run(tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				dp, err := pattern.Discover(tc.Lines, opts)
				if err != nil {
					b.Fatalf("%v", err)
				}
				if dp == nil || dp.Grok == "" {
					b.Fatalf("empty result")
				}
			}
		})
	}
}
