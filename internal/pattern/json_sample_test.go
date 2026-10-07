package pattern

import "testing"

func TestJSONSampleMemoizes(t *testing.T) {
	lines := []string{
		`{"a":1}`,
		`not json`,
		`{"b":"two"}`,
		`{"a":1}`, // duplicate content, separate parse (index-based memo)
	}
	js := newJSONSample(lines)
	if js.Count() != 4 {
		t.Fatalf("count=%d", js.Count())
	}
	obj, ok := js.Obj(0)
	if !ok || obj["a"] != float64(1) {
		t.Fatalf("Obj(0)=%v ok=%v", obj, ok)
	}
	if _, ok := js.Obj(1); ok {
		t.Fatal("Obj(1) should not parse")
	}
	obj2, ok := js.Obj(2)
	if !ok || obj2["b"] != "two" {
		t.Fatalf("Obj(2)=%v ok=%v", obj2, ok)
	}
	// Memoized re-access returns the same map.
	objAgain, ok := js.Obj(0)
	if !ok || objAgain["a"] != float64(1) {
		t.Fatal("memoized re-access broken")
	}
	if len(js.Lines()) != len(lines) || &js.Lines()[0] != &lines[0] {
		t.Fatal("Lines() must expose the original sample slice")
	}
}
