package pattern

import (
	"io"
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

	dp := tiledUnion(shapes, eval, 0.75, io.Discard)
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

	relaxed := relaxTail(best, eval)
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

	if dp := tryTextEnvelope(sample, all, io.Discard); dp != nil {
		t.Fatalf("tryTextEnvelope returned weak candidate coverage %.3f", dp.Coverage)
	}
}
