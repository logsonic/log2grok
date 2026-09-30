package pattern

import (
	"io"
	"strings"
	"testing"
)

func TestTilerCachesResetAfterPrimitiveReplacement(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	if got := classifyToken("WARN").GrokName; got != "LOGLEVEL" {
		t.Fatalf("initial classifyToken(WARN) = %s, want LOGLEVEL", got)
	}

	primitives := GrokPrimitivesSnapshot()
	primitives["LOGLEVEL"] = `INFO`
	ReplaceGrokPrimitives(primitives)

	if got := classifyToken("INFO").GrokName; got != "LOGLEVEL" {
		t.Fatalf("classifyToken(INFO) after replacement = %s, want LOGLEVEL", got)
	}
	if got := classifyToken("WARN").GrokName; got == "LOGLEVEL" {
		t.Fatalf("classifyToken(WARN) used stale LOGLEVEL primitive")
	}
}

// Composite expression regexes are precompiled into a version-checked slice
// parallel to tileComposites. They must match the old exprFullMatch behavior
// and must be rebuilt when the primitive table changes (not cached stale).
func TestCompositeExprsTrackPrimitiveReplacement(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	ensureCompExprs()
	if len(compExprRe) != len(tileComposites) {
		t.Fatalf("compExprRe len %d != tileComposites %d", len(compExprRe), len(tileComposites))
	}
	if compExprRe[1] == nil {
		t.Fatal("ISO8601 composite expr failed to compile")
	}
	if !compExprRe[1].MatchString("2025-01-15T10:23:45Z") {
		t.Fatal("ISO8601 composite does not match an ISO timestamp")
	}

	// A primitive-table replacement must advance the version and rebuild the
	// cache, not serve a stale regex.
	verBefore := compExprVer
	primitives := GrokPrimitivesSnapshot()
	primitives["TIMESTAMP_ISO8601"] = `NOT_A_TIMESTAMP`
	ReplaceGrokPrimitives(primitives)
	ensureCompExprs()
	if compExprVer == verBefore {
		t.Fatal("compExprVer did not advance after primitive replacement")
	}
	if compExprRe[1] != nil && compExprRe[1].MatchString("2025-01-15T10:23:45Z") {
		t.Fatal("compExprRe[1] still uses the replaced TIMESTAMP_ISO8601 primitive")
	}

	// resetTilerCaches must clear and rebuild cleanly.
	resetTilerCaches()
	ensureCompExprs()
	if len(compExprRe) != len(tileComposites) {
		t.Fatalf("compExprRe len %d != tileComposites %d after reset", len(compExprRe), len(tileComposites))
	}
}

// resetTilerCaches runs concurrently with Discover (it is called by the
// documented config-replacement APIs). matchComposite and classifyToken index
// the compiled slices under their read locks, so a reset must not leave an
// empty slice for a reader that validated the cache just before the reset.
// This pins the invariant directly; the earlier real-time interleaving could
// only be reproduced by a sustained reset storm.
func TestTilerResetKeepsIndexedCachesPopulated(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	ensureCompExprs()
	ensureTileSingles()
	if len(compExprRe) != len(tileComposites) || len(tileSingles) == 0 {
		t.Fatal("caches not populated before reset")
	}

	resetTilerCaches()

	if len(compExprRe) != len(tileComposites) {
		t.Fatalf("resetTilerCaches left compExprRe len %d, want %d (matchComposite would panic)",
			len(compExprRe), len(tileComposites))
	}
	if len(tileSingles) == 0 {
		t.Fatal("resetTilerCaches left tileSingles empty (classifyToken would panic)")
	}
}

func TestTiledUnionDoesNotConsumeSkippedBranchMatches(t *testing.T) {
	eval := []string{
		"common0 1",
		"common1 1",
		"common2 1",
		"common3 1",
		"common4 1",
		"common5 1",
		"new6 1",
		"new7 1",
	}
	shapes := []*tileCandidate{
		{
			Grok:     `(?:common0|common1|common2|common3|common4|common5) %{INT:n}`,
			Matched:  6,
			Promoted: 1,
		},
		{
			Grok:     `(?:common0|common1|common2|common3|common4|new6) %{INT:n}`,
			Matched:  6,
			Promoted: 1,
		},
		{
			Grok:     `(?:new6|new7) %{INT:n}`,
			Matched:  2,
			Promoted: 1,
		},
	}

	dp := tiledUnion(shapes, eval, 0.75, io.Discard, nil)
	if dp == nil {
		t.Fatal("tiledUnion returned nil; skipped branch consumed new6")
	}
	if dp.MatchedCount != len(eval) {
		t.Fatalf("union matched %d/%d, want full coverage", dp.MatchedCount, len(eval))
	}
}

func TestRelaxTailAcceptsMinimumGain(t *testing.T) {
	eval := make([]string, 0, 100)
	for i := 0; i < 98; i++ {
		eval = append(eval, "1 ok")
	}
	eval = append(eval, "1 not ok", "1 still not ok")

	best := &tileCandidate{
		Matched: 98,
		Total:   len(eval),
		pieces: []tilePiece{
			fieldPiece(classifyToken("1")),
			litPiece(" "),
			fieldPiece(classifyToken("ok")),
		},
	}

	relaxed := relaxTail(best, eval, nil)
	if relaxed == nil {
		t.Fatal("relaxTail rejected an exact minimum-gain improvement")
	}
	if relaxed.Matched != len(eval) {
		t.Fatalf("relaxed matched %d/%d, want full coverage", relaxed.Matched, len(eval))
	}
}

func TestTextEnvelopeSkipsWeakFullInputCoverage(t *testing.T) {
	sample := []string{
		`2026/06/02 07:35:26 INFO worker.alpha: started`,
		`2026/06/02 07:35:27 WARN worker.alpha: retrying`,
		`2026/06/02 07:35:28 ERROR worker.beta: failed`,
		`2026/06/02 07:35:29 DEBUG worker.gamma: checkpoint`,
	}
	all := append([]string{}, sample...)
	all = append(all,
		`unrelated one`,
		`unrelated two`,
		`unrelated three`,
		`unrelated four`,
		`unrelated five`,
		`unrelated six`,
	)

	if dp := tryTextEnvelope(sample, all, io.Discard, nil); dp != nil {
		t.Fatalf("tryTextEnvelope returned weak candidate coverage %.3f", dp.Coverage)
	}
}

// relaxTail must still find the relaxation that buys real coverage for a
// rigid-prefix/varying-tail log, now that cut-point scans are floor-pruned.
func TestRelaxTailFindsCoverageGain(t *testing.T) {
	eval := []string{
		"2025-01-15T10:23:45Z INFO worker started",
		"2025-01-15T10:23:46Z INFO job 1 done",
		"2025-01-15T10:23:47Z INFO job 2 done",
		"2025-01-15T10:23:48Z INFO shutdown",
		"2025-01-15T10:23:49Z INFO bye",
	}
	sample := eval
	shapes := tileShapes(sample, eval, tileShapeTemplates, nil)
	best := bestTiling(shapes, eval, nil)
	if best == nil {
		t.Fatal("no tiling")
	}
	relaxed := relaxTail(best, eval, nil)
	// Either no relaxation buys >=2% (acceptable), or the relaxed candidate
	// strictly beats best and carries a GREEDYDATA message tail.
	if relaxed != nil {
		if relaxed.Matched <= best.Matched {
			t.Fatalf("relaxed=%d must beat best=%d", relaxed.Matched, best.Matched)
		}
		if !strings.Contains(relaxed.Grok, "%{GREEDYDATA:message}") {
			t.Fatalf("relaxed grok lacks message tail: %s", relaxed.Grok)
		}
	}
}
