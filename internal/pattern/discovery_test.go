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
	if strings.HasPrefix(dp.Source, "inferred:multi(") {
		t.Fatalf("source = %q, want fallback, not a literal union", dp.Source)
	}
	if strings.Contains(dp.Grok, "ALERT backend/api") || strings.Contains(dp.Grok, "|(?:WARN cache miss") {
		t.Fatalf("grok is a literal sample alternation: %q", dp.Grok)
	}
	if dp.Grok != `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want log-level fallback", dp.Grok)
	}
}

func TestDiscoverSingleUnknownLineDoesNotReturnLiteralTiling(t *testing.T) {
	lines := []string{`GET /api/users 200 12ms`}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.SourceFamily == "inferred" {
		t.Fatalf("source = %q, want fallback for single unknown line", dp.Source)
	}
	if dp.Grok != `%{WORD:method}\s+%{URIPATHPARAM:url}\s+%{INT:status}(?:\s+%{DURATION:duration})?` {
		t.Fatalf("grok = %q, want HTTP summary fallback", dp.Grok)
	}
}

func TestDiscoverShortShapeOnlyAlignmentFallsBackToLogLevelMessage(t *testing.T) {
	lines := []string{
		`INFO api server started`,
		`INFO api cache warmed`,
		`INFO worker job finished`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	// Three INFO lines align structurally but share no vocabulary beyond
	// the level token; a short sample without shared keywords must fall
	// back rather than freeze an accidental shape.
	if dp.SourceFamily == "inferred" {
		t.Fatalf("source = %q, want fallback instead of shape-only short-sample tiling", dp.Source)
	}
	if dp.Grok != `%{LOGLEVEL:level}\s+%{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want log-level fallback", dp.Grok)
	}
}

func TestDiscoverInfersTextEnvelopeForDMYBracketedLevel(t *testing.T) {
	lines := []string{
		`02-06-2026 07:35:26.253 [WARNING] SAI_API_SWITCH: Warning: Missing "default_pll". Use SDK default values.`,
		`02-06-2026 07:35:26.254 [DEBUG] SAI_API_QUEUE: SAI_API_GET::get_queue_attribute(id 0x1500000000097A|SAI_QUEUE_STAT_PACKETS)`,
		`02-06-2026 07:36:19.123 [ERR] src/cgm/hal/gseries/buffer/pool/ingress_buffer_pool_gseries.cpp:435: SAI_BUFFER_POOL_STAT_XOFF_ROOM_WATERMARK_BYTES is not supported on G200`,
		`02-06-2026 07:36:19.124 [INFO] SAI_API_BUFFER: src/cgm/hal/gseries/buffer/ingress_priority_group_stats_manager_gseries.cpp:47: Skipping SQ watermark query for IPG 0xabc default buffer profile`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "inferred:Text Envelope" {
		t.Fatalf("source = %q, want generic text envelope probe", dp.Source)
	}
	want := `%{MONTHDAY2:day}-%{MONTHNUM2:month}-%{YEAR:year} %{TIME:time}\s+\[%{LOGLEVEL:level}\]\s+%{NOTSPACE:component}:\s*%{GREEDYDATA:message}`
	if dp.Grok != want {
		t.Fatalf("grok = %q, want %q", dp.Grok, want)
	}
	re, err := CompileGrok(dp.Grok, nil)
	if err != nil {
		t.Fatalf("CompileGrok returned error: %v", err)
	}
	if matched := EvaluateCoverage(re, lines); matched != len(lines) {
		t.Fatalf("coverage = %d/%d, want full coverage", matched, len(lines))
	}
}

func TestDiscoverInfersTextEnvelopeForSlashDateLevelComponent(t *testing.T) {
	lines := []string{
		`2026/06/02 07:35:26.253 INFO worker.alpha: started job=42`,
		`2026/06/02 07:35:27.100 WARN worker.alpha: retrying job=42 delay=10ms`,
		`2026/06/02 07:35:28.005 ERROR worker.beta: failed job=13 err=timeout`,
		`2026/06/02 07:35:29.250 DEBUG worker.gamma: checkpoint saved`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "inferred:Text Envelope" {
		t.Fatalf("source = %q, want generic text envelope probe", dp.Source)
	}
	want := `%{YEAR:year}/%{MONTHNUM2:month}/%{MONTHDAY2:day} %{TIME:time}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:component}:\s*%{GREEDYDATA:message}`
	if dp.Grok != want {
		t.Fatalf("grok = %q, want %q", dp.Grok, want)
	}
	re, err := CompileGrok(dp.Grok, nil)
	if err != nil {
		t.Fatalf("CompileGrok returned error: %v", err)
	}
	if matched := EvaluateCoverage(re, lines); matched != len(lines) {
		t.Fatalf("coverage = %d/%d, want full coverage", matched, len(lines))
	}
}

func TestDiscoverPrefersLibraryOverInferredTextEnvelope(t *testing.T) {
	lines := []string{
		`2026-04-29T01:01:00.000Z [INFO]  agent: Synced service "web-1"`,
		`2026-04-29T02:02:00.000Z [INFO]  agent: Synced service "web-2"`,
		`2026-04-29T03:03:00.000Z [INFO]  agent: Synced service "web-3"`,
	}

	dp, err := Discover(lines, Options{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if dp.Source != "library:Consul Log" {
		t.Fatalf("source = %q, want curated library pattern", dp.Source)
	}
	if dp.Grok != `%{TIMESTAMP_ISO8601:timestamp} \[%{LOGLEVEL:level}\]\s+agent: %{GREEDYDATA:message}` {
		t.Fatalf("grok = %q, want Consul library pattern", dp.Grok)
	}
}

// When stage 1 (structured) auto-accepts, the library and inferred stages
// must be aborted. The result must still be the stage-1 candidate,
// byte-identical to the pre-cancellation behavior. We assert on the
// coordinator's abort decision (stageAbortHook), which is synchronous and
// therefore deterministic — a scan-level observation would only fire if a
// lower stage happened to still be mid-scan.
func TestDiscoverAbortsLowerStagesOnStructuredAutoAccept(t *testing.T) {
	oldCap := coverageEvalCap
	coverageEvalCap = 100000 // keep the exact-coverage path
	defer func() { coverageEvalCap = oldCap }()

	lines := []string{
		`{"ts":"2025-01-15T10:23:45Z","level":"info","msg":"job 1"}`,
		`{"ts":"2025-01-15T10:23:46Z","level":"info","msg":"job 2"}`,
		`{"ts":"2025-01-15T10:23:47Z","level":"info","msg":"job 3"}`,
	}
	opts := Options{LibraryThreshold: 0.85}

	aborted := make(chan string, 8)
	oldHook := stageAbortHook
	stageAbortHook = func(stage string) { aborted <- stage }
	defer func() { stageAbortHook = oldHook }()

	dp, err := Discover(lines, opts)
	if err != nil {
		t.Fatal(err)
	}
	if dp.SourceFamily != "structured" {
		t.Fatalf("expected structured auto-accept, got %s (%s)", dp.SourceFamily, dp.Source)
	}
	got := map[string]bool{}
	for {
		select {
		case stage := <-aborted:
			got[stage] = true
			continue
		default:
		}
		break
	}
	if !got["library"] || !got["inferred"] {
		t.Fatalf("expected library and inferred to be aborted, got %v", got)
	}
}

// And the inverse: with NO auto-accept, no stage may be aborted (aborted
// partial counts would poison pickBetter).
func TestDiscoverNoAbortWithoutAutoAccept(t *testing.T) {
	oldCap := coverageEvalCap
	coverageEvalCap = 100000
	defer func() { coverageEvalCap = oldCap }()

	// Heterogeneous lines: no stage reaches the 0.85 threshold.
	lines := []string{
		"user alice logged in from 10.0.0.1",
		"backup finished with status 0",
		"cache: 512 entries evicted",
		"temperature reading 21.5 celsius",
	}
	opts := Options{LibraryThreshold: 0.999}

	aborted := make(chan string, 8)
	oldHook := stageAbortHook
	stageAbortHook = func(stage string) { aborted <- stage }
	defer func() { stageAbortHook = oldHook }()

	if _, err := Discover(lines, opts); err != nil {
		t.Fatal(err)
	}
	select {
	case stage := <-aborted:
		t.Fatalf("stage %q was aborted although no stage auto-accepted", stage)
	default:
	}
}
