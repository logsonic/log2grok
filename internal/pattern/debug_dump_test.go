package pattern

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// Manual inspection aid for tiler output on golden-corpus cases:
//
//	TILE_DUMP=haproxy_http_001,redis_001 go test -run TestTileDump -v
//
// Skipped unless TILE_DUMP is set; it exists so a regression in the
// inference engine can be eyeballed against the expert grok in seconds.
func TestTileDump(t *testing.T) {
	env := os.Getenv("TILE_DUMP")
	if env == "" {
		t.Skip("set TILE_DUMP=case1,case2")
	}
	for name := range strings.SplitSeq(env, ",") {
		input, err := loadCaseInput(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		expected, _ := loadExpectedGrok(name)
		fmt.Printf("=== %s ===\nline:     %s\nexpected: %s\n", name, input[0], expected)
		if dp := tryTiling(input, input, io.Discard, nil); dp != nil {
			fmt.Printf("tiler:    %s\ncoverage: %.2f\n\n", dp.Grok, dp.Coverage)
		} else {
			fmt.Printf("tiler:    <nil>\n\n")
		}
	}
}
