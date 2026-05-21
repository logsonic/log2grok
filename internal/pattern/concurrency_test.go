package pattern

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentDiscoverNoRace runs many overlapping Discover and
// DiscoverMulti calls in parallel. It guards against the shared-drain-
// backend race: a higher-priority stage can auto-accept and return while
// the drain goroutine is still training in the background, so independent
// calls can have overlapping drain work. Run with -race to be meaningful.
func TestConcurrentDiscoverNoRace(t *testing.T) {
	inputs := [][]string{
		genConcNginx(60),
		genConcJSON(60),
		genConcMixed(80),
	}

	var wg sync.WaitGroup
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			lines := inputs[g%len(inputs)]
			if g%2 == 0 {
				if _, err := Discover(lines, Options{LibraryThreshold: 0.75}); err != nil {
					t.Errorf("Discover: %v", err)
				}
			} else {
				if _, err := DiscoverMulti(lines, Options{LibraryThreshold: 0.85, TargetCoverage: 0.9}); err != nil {
					t.Errorf("DiscoverMulti: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()
}

func genConcNginx(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i)
	}
	return out
}

func genConcJSON(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`{"ts":"2025-01-15T10:23:%02dZ","level":"info","msg":"job %d"}`, i%60, i)
	}
	return out
}

func genConcMixed(n int) []string {
	out := make([]string, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = fmt.Sprintf(`WORKER queue=email processed=%d latency=%dms`, i, i*3)
		} else {
			out[i] = fmt.Sprintf(`10.0.0.%d - - [15/Jan/2025:10:23:%02d +0000] "GET /p%d HTTP/1.1" 200 %d`, i%256, i%60, i%100, 100+i)
		}
	}
	return out
}

// TestDiscoverEdgeCases exercises empty, blank-only, and single-line input.
func TestDiscoverEdgeCases(t *testing.T) {
	if _, err := Discover(nil, Options{}); err != ErrEmptyInput {
		t.Fatalf("nil input: err = %v, want ErrEmptyInput", err)
	}
	if _, err := Discover([]string{"", "", ""}, Options{}); err != ErrEmptyInput {
		t.Fatalf("blank-only input: err = %v, want ErrEmptyInput", err)
	}
	dp, err := Discover([]string{`10.0.0.1 - - [15/Jan/2025:10:23:45 +0000] "GET / HTTP/1.1" 200 1`}, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("single line: %v", err)
	}
	if dp == nil || dp.Grok == "" {
		t.Fatalf("single line produced empty result")
	}

	if _, err := DiscoverMulti(nil, Options{}); err != ErrEmptyInput {
		t.Fatalf("DiscoverMulti nil: err = %v, want ErrEmptyInput", err)
	}
}
