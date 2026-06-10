package pattern

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This harness measures GENERALIZATION, not library recall. The golden
// `expected.grok` files are hand-written by experts — they are the closest
// thing we have to an "LLM-level" answer. We disable the library and
// structured probes entirely and ask: can the inference engine (the tiler)
// reconstruct the expert's FIELD STRUCTURE from scratch?
//
// The metric is field-level, not coverage. Coverage is gamed by GREEDYDATA
// (one catch-all field "matches" every line at 100% while recovering
// nothing), so we compare the ordered sequence of HIGH-INFORMATION semantic
// field classes (timestamp, ip, number, level, id, path, duration) against
// the expected grok via longest-common-subsequence:
//
//	recovery = LCS(expected_hi, produced_hi) / len(expected_hi)
//
// "free" fields (GREEDYDATA / DATA / NOTSPACE / WORD / USER / quoted) are
// excluded from the score: both sides always have them and they carry no
// structural information.

// semanticClass maps a Grok primitive name to a coarse semantic class. The
// empty string means "free / low-information" and is excluded from scoring.
func semanticClass(grokName string) string {
	switch strings.ToUpper(grokName) {
	case "TIMESTAMP_ISO8601", "HTTPDATE", "SYSLOGTIMESTAMP", "DATESTAMP", "DATESTAMP_RFC822",
		"DATESTAMP_RFC2822", "DATESTAMP_OTHER", "DATESTAMP_EVENTLOG", "DATE", "DATE_US", "DATE_EU",
		"TIME", "ISO8601", "UNIX", "UNIXMS", "MONTH", "MONTHDAY", "MONTHNUM", "MONTHNUM2",
		"YEAR", "HOUR", "MINUTE", "SECOND", "CISCOTIMESTAMP", "TZ":
		return "ts"
	case "IP", "IPV4", "IPV6", "IPORHOST", "HOSTNAME", "HOST", "HOSTPORT", "MAC":
		return "ip"
	case "INT", "NUMBER", "FLOAT", "NONNEGINT", "POSINT", "BASE10NUM", "INTEGER":
		return "num"
	case "LOGLEVEL", "REDISLEVEL", "LEVEL", "SYSLOGLEVEL":
		return "level"
	case "UUID", "TRACEID", "SPANID":
		return "id"
	case "URI", "URIPATH", "URIPATHPARAM", "PATH", "URIPARAM", "URIHOST", "URN":
		return "path"
	case "DURATION":
		return "dur"
	case "EMAILADDRESS", "EMAIL":
		return "email"
	default:
		return "" // free / low-information
	}
}

// hiClassSeq extracts the ordered high-information class sequence from a grok.
func hiClassSeq(grok string) []string {
	var out []string
	for _, m := range grokRefRe.FindAllStringSubmatch(grok, -1) {
		if c := semanticClass(m[1]); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func lcsLen(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// recovery scores a produced grok against the expected grok: fraction of the
// expected high-info field classes recovered in order. over is the number of
// produced high-info fields beyond what the LCS credited (a precision cost).
func recovery(expected, produced string) (score float64, expHi int, over int) {
	exp := hiClassSeq(expected)
	prod := hiClassSeq(produced)
	expHi = len(exp)
	if expHi == 0 {
		return 1.0, 0, 0 // nothing structural to recover
	}
	l := lcsLen(exp, prod)
	over = len(prod) - l
	if over < 0 {
		over = 0
	}
	return float64(l) / float64(expHi), expHi, over
}

func loadCaseInput(name string) ([]string, error) {
	p := filepath.Join("..", "..", "test", "benchmark", "cases", name, "input.log")
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if sc.Text() != "" {
			out = append(out, sc.Text())
		}
	}
	return out, sc.Err()
}

func loadExpectedGrok(name string) (string, error) {
	p := filepath.Join("..", "..", "test", "benchmark", "cases", name, "expected.grok")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func allCaseNames(t *testing.T) []string {
	dir := filepath.Join("..", "..", "test", "benchmark", "cases")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cases dir: %v", err)
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// isStructuredCase reports whether the case is handled by a structured probe
// (JSON / logfmt / CSV / TSV / CEF / W3C). Those are not the tiler's job,
// so they are reported separately.
func isStructuredCase(input []string, expected string) bool {
	if strings.Contains(expected, `%{GREEDYDATA:json}`) {
		return true
	}
	if s := tryStructured(input, input, io.Discard); s != nil && s.Coverage >= 0.9 {
		return true
	}
	return false
}

// tilerRecoveryFloor is the regression gate: the mean field recovery the
// tiler must sustain over the text/positional golden cases. Measured at
// 0.999 when the engine was redesigned (drain managed 0.238 on the same
// corpus before it was removed); the floor leaves slack for corpus growth.
const tilerRecoveryFloor = 0.95

// TestFieldRecoveryHarness is the measurement instrument and regression
// gate. It prints a per-case table and the aggregate mean recovery for the
// tiler over the text/positional cases (library + structured disabled), and
// fails if the mean drops below tilerRecoveryFloor. Run with -v for the
// table.
func TestFieldRecoveryHarness(t *testing.T) {
	names := allCaseNames(t)

	type row struct {
		name    string
		expHi   int
		rec     float64
		over    int
		nilTile bool
	}
	var rows []row
	var sum float64
	var textN int

	for _, name := range names {
		input, err := loadCaseInput(name)
		if err != nil || len(input) == 0 {
			continue
		}
		expected, err := loadExpectedGrok(name)
		if err != nil {
			continue
		}
		if isStructuredCase(input, expected) {
			continue
		}
		// Skip cases with no structural fields to recover (pure GREEDYDATA
		// expectations) — they don't discriminate.
		if len(hiClassSeq(expected)) == 0 {
			continue
		}

		r := row{name: name, expHi: len(hiClassSeq(expected))}
		if tc := tryTiling(input, input, io.Discard); tc != nil {
			r.rec, _, r.over = recovery(expected, tc.Grok)
		} else {
			r.nilTile = true
		}
		rows = append(rows, r)
		sum += r.rec
		textN++
	}

	fmt.Printf("\n%-30s %5s %7s %6s\n", "case", "expHi", "tiler", "over")
	fmt.Println(strings.Repeat("-", 52))
	for _, r := range rows {
		rec := fmt.Sprintf("%.2f", r.rec)
		if r.nilTile {
			rec = "nil"
		}
		fmt.Printf("%-30s %5d %7s %6d\n", r.name, r.expHi, rec, r.over)
	}
	fmt.Println(strings.Repeat("-", 52))
	fmt.Printf("text/positional cases: %d\n", textN)
	if textN == 0 {
		t.Fatal("no text/positional cases found")
	}
	mean := sum / float64(textN)
	fmt.Printf("MEAN field recovery — tiler: %.3f (floor %.2f)\n", mean, tilerRecoveryFloor)
	if mean < tilerRecoveryFloor {
		t.Fatalf("mean field recovery %.3f fell below the %.2f floor", mean, tilerRecoveryFloor)
	}
}
