package pattern

// jsonSample memoizes parsed JSON objects for one sample slice so the
// structured probes parse each line at most once per Discover call.
// encoding/json into map[string]any is allocation-heavy; the probes
// previously re-parsed the same lines 6-9 times per call
// (jsonObjectFraction, keyFreq, observeJSONShape, plus per-probe Likely).
type jsonSample struct {
	lines []string
	objs  []map[string]any
	seen  []bool // line has been parsed (valid or not); guards memoization
}

func newJSONSample(lines []string) *jsonSample {
	return &jsonSample{
		lines: lines,
		objs:  make([]map[string]any, len(lines)),
		seen:  make([]bool, len(lines)),
	}
}

// Lines returns the sample lines (for probes that only need raw text).
func (js *jsonSample) Lines() []string { return js.lines }

// Count returns the number of sample lines.
func (js *jsonSample) Count() int { return len(js.lines) }

// Obj returns the parsed JSON object for sample line i, parsing on first
// access (ok=false when the line is not a JSON object). Memoization is
// keyed on "already attempted", so a non-JSON line is never re-parsed.
// Not safe for concurrent use: probes run sequentially inside tryStructured.
func (js *jsonSample) Obj(i int) (map[string]any, bool) {
	if !js.seen[i] {
		js.objs[i], _ = parseJSONLine(js.lines[i])
		js.seen[i] = true
	}
	obj := js.objs[i]
	return obj, obj != nil
}
