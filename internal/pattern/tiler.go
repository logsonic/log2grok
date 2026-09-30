package pattern

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The tiler is the from-scratch inference engine for unknown formats. It is
// what a human writing a Grok by hand does, mechanised:
//
//  1. SEGMENT one representative ("template") line into units: literal
//     delimiter runs, quoted regions, multi-token composite timestamps, and
//     plain tokens. Composite timestamps are matched as ONE unit because
//     their regexes span the embedded spaces.
//  2. PROBE the whole sample with a relaxed alignment regex (every token
//     becomes (\S+), every quoted region "([^"]*)") and collect the observed
//     VALUE SET of every slot. Structure comes from the template line;
//     semantics come from the population.
//  3. TYPE each slot by voting the most specific primitive that matches all
//     observed values. A slot whose values never vary is a keyword → literal.
//     A slot whose values share an internal separator signature (192.0.1.1:443,
//     0/0/1/2/3, HTTP/1.1) is recursively SUB-TILED into typed columns. A slot
//     that is sometimes "-" gets a dash alternation: (?:%{INT:bytes}|-).
//     A quoted region whose inner structure is stable is tiled inside;
//     otherwise it becomes %{DATA}.
//  4. BRUTE-FORCE over several distinct template lines, score each tiling on
//     the full input, and keep the best. If the winner still leaves lines
//     unexplained, try relaxing the tail into %{GREEDYDATA:message} at each
//     field boundary and keep the relaxation only when it buys real coverage.

// ---------------------------------------------------------------------------
// Composite (multi-token) primitives
// ---------------------------------------------------------------------------

// tileComposite is a prefix-anchored shape that may span embedded whitespace
// (timestamps), so it cannot be recognised by whole-token classification.
// Pattern is the hand recogniser; Expr is the Grok expression rendered for
// the span. A composite is accepted only when the compiled Expr full-matches
// the span it selected, so the emitted Grok can never disagree with the
// recogniser.
type tileComposite struct {
	Pattern string
	Expr    string
	prefix  *regexp.Regexp
}

func tc(pattern, expr string) *tileComposite {
	return &tileComposite{
		Pattern: pattern,
		Expr:    expr,
		prefix:  regexp.MustCompile("^(?:" + pattern + ")"),
	}
}

// Ordered most-specific first. Variants that differ only in day/month order
// are both listed; Expr validation picks the one whose strict primitives
// accept the span (e.g. 29-04-2026 can only be day-first).
var tileComposites = []*tileComposite{
	tc(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})? (?:UTC|GMT|[ECMP][SD]T)`,
		`%{TIMESTAMP_ISO8601:timestamp} %{TZ:tz}`),
	tc(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`,
		`%{TIMESTAMP_ISO8601:timestamp}`),
	tc(`\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}`,
		`%{HTTPDATE:timestamp}`),
	tc(`\d{2}/[A-Za-z]{3}/\d{4} \d{2}:\d{2}:\d{2}`,
		`%{HTTPDATE_CONDENSED:timestamp}`),
	tc(`(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}`,
		`%{SYSLOGTIMESTAMP:timestamp}`),
	tc(`\d{1,2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{4} \d{2}:\d{2}:\d{2}`,
		`%{MONTHDAY:monthday} %{MONTH:month} %{YEAR:year} %{TIME:time}`),
	tc(`\d{4}/\d{2}/\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{YEAR:year}/%{MONTHNUM2:month}/%{MONTHDAY2:monthday} %{TIME:time}`),
	tc(`\d{4}\.\d{2}\.\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{YEAR:year}\.%{MONTHNUM2:month}\.%{MONTHDAY2:monthday} %{TIME:time}`),
	tc(`\d{2}-\d{2}-\d{4}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHDAY2:day}-%{MONTHNUM2:month}-%{YEAR:year} %{TIME:time}`),
	tc(`\d{2}-\d{2}-\d{4}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHNUM2:month}-%{MONTHDAY2:day}-%{YEAR:year} %{TIME:time}`),
	tc(`\d{2}/\d{2}/\d{4}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHDAY2:day}/%{MONTHNUM2:month}/%{YEAR:year} %{TIME:time}`),
	tc(`\d{2}/\d{2}/\d{4}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHNUM2:month}/%{MONTHDAY2:day}/%{YEAR:year} %{TIME:time}`),
	tc(`\d{2}/\d{2}-\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHNUM2:month}/%{MONTHDAY2:monthday}-%{TIME:time}`),
	tc(`\d{1,2}-(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)-\d{4}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`,
		`%{MONTHDAY:monthday}-%{MONTH:month}-%{YEAR:year} %{TIME:time}`),
}

var (
	compExprMu     sync.RWMutex
	compExprVer    uint64
	compExprRe     []*regexp.Regexp // parallel to tileComposites; nil = compile failed
	compExprLoaded bool
)

// ensureCompExprs compiles every composite's Expr once per primitive-table
// version. Called lazily (primitives may load after tiler var init) and on
// the hot path of matchComposite — RLock-only after the first build.
func ensureCompExprs() {
	version := currentPatternStateVersion()
	compExprMu.RLock()
	ok := compExprLoaded && compExprVer == version
	compExprMu.RUnlock()
	if ok {
		return
	}

	built := make([]*regexp.Regexp, len(tileComposites))
	for i, c := range tileComposites {
		re, err := CompileGrok(c.Expr, nil)
		if err != nil {
			re = nil // failed composites never match (old exprFullMatch semantics)
		}
		built[i] = re
	}

	compExprMu.Lock()
	defer compExprMu.Unlock()
	if compExprLoaded && compExprVer == version {
		return
	}
	compExprRe = built
	compExprVer = version
	compExprLoaded = true
}

func resetTilerCaches() {
	compExprMu.Lock()
	compExprVer = 0
	compExprRe = nil
	compExprLoaded = false
	compExprMu.Unlock()

	tileSinglesMu.Lock()
	tileSinglesVersion = 0
	tileSingles = nil
	tileDataPrim = nil
	tileHostnamePrim = nil
	tileSinglesMu.Unlock()
}

// matchComposite tries each composite at offset i, returning the first whose
// prefix match ends on a token boundary AND whose Expr full-matches the span.
func matchComposite(line string, i int) (*tileComposite, int) {
	ensureCompExprs()
	compExprMu.RLock()
	defer compExprMu.RUnlock()

	rest := line[i:]
	for ci, c := range tileComposites {
		loc := c.prefix.FindStringIndex(rest)
		if loc == nil || loc[1] == 0 {
			continue
		}
		end := i + loc[1]
		// A composite must end at a token boundary or just before an
		// intra-token separator (`12:01:02,000:` — the trailing colon is a
		// label separator, not part of the timestamp).
		if end < len(line) && !tileDelims[line[end]] && !tileSubSeps[line[end]] {
			continue
		}
		if compExprRe[ci] == nil || !compExprRe[ci].MatchString(rest[:loc[1]]) {
			continue
		}
		return c, loc[1]
	}
	return nil, 0
}

// ---------------------------------------------------------------------------
// Single-token primitives
// ---------------------------------------------------------------------------

// tilePrimitive is a whole-token classifier backed by a real Grok primitive.
type tilePrimitive struct {
	GrokName string
	Regex    *regexp.Regexp
	NameHint string
	// Promote marks unambiguous semantic types (timestamps, IPs, numbers,
	// ids, levels) that stay fields even when constant in the sample; a
	// constant WORD/NOTSPACE is a keyword and is demoted to a literal.
	Promote bool
}

// tileSingleSpecs names the single-token primitives the tiler recognises,
// ordered most-specific → least. The classifier regex for each is derived
// from the *actual* Grok primitive (via CompileGrok at first use), so a
// token classified as TYPE is guaranteed to match the emitted %{TYPE} — the
// classifier and the renderer can never disagree. NOTSPACE is the guaranteed
// catch-all.
type tileSingleSpec struct {
	GrokName string
	NameHint string
	Promote  bool
}

var tileSingleSpecs = []tileSingleSpec{
	{"UUID", "id", true},
	{"MAC", "mac", true},
	{"IPV4", "ip", true},
	{"IPV6", "ip", true},
	{"EMAILADDRESS", "email", true},
	{"URI", "url", true},
	{"TIMESTAMP_ISO8601", "timestamp", true},
	{"DATE", "date", true},
	{"TIME", "time", true},
	{"LOGLEVEL", "level", true},
	{"DURATION", "duration", true},
	{"URIPATHPARAM", "path", true},
	{"INT", "n", true},
	{"NUMBER", "n", true},
	{"REDISLEVEL", "level", true},
	{"WORD", "word", false},
	{"NOTSPACE", "value", false},
}

var (
	tileSinglesMu      sync.RWMutex
	tileSinglesVersion uint64
	tileSingles        []tilePrimitive
	tileDataPrim       *tilePrimitive
	tileHostnamePrim   *tilePrimitive
)

// loadTileSingles compiles each single-token classifier from its real Grok
// primitive. Done lazily so it runs after the embedded primitive table is
// loaded, regardless of package init order. A primitive that fails to
// compile (or is absent) is skipped.
func loadTileSingles() ([]tilePrimitive, *tilePrimitive, *tilePrimitive) {
	var singles []tilePrimitive
	for _, spec := range tileSingleSpecs {
		re, err := CompileGrok("%{"+spec.GrokName+"}", nil)
		if err != nil {
			continue
		}
		singles = append(singles, tilePrimitive{
			GrokName: spec.GrokName,
			NameHint: spec.NameHint,
			Promote:  spec.Promote,
			Regex:    re,
		})
	}
	dataRe, err := CompileGrok("%{DATA}", nil)
	if err != nil {
		dataRe = regexp.MustCompile(`^[\s\S]*$`)
	}
	dataPrim := &tilePrimitive{GrokName: "DATA", NameHint: "text", Regex: dataRe}
	var hostPrim *tilePrimitive
	if hostRe, err := CompileGrok("%{HOSTNAME}", nil); err == nil {
		hostPrim = &tilePrimitive{GrokName: "HOSTNAME", NameHint: "hostname", Promote: true, Regex: hostRe}
	}
	if len(singles) == 0 {
		singles = append(singles, tilePrimitive{
			GrokName: "NOTSPACE",
			NameHint: "value",
			Regex:    regexp.MustCompile(`^\S+$`),
		})
	}
	return singles, dataPrim, hostPrim
}

func ensureTileSingles() {
	version := currentPatternStateVersion()
	tileSinglesMu.RLock()
	ok := len(tileSingles) > 0 && tileSinglesVersion == version
	tileSinglesMu.RUnlock()
	if ok {
		return
	}

	singles, dataPrim, hostPrim := loadTileSingles()

	tileSinglesMu.Lock()
	defer tileSinglesMu.Unlock()
	if len(tileSingles) > 0 && tileSinglesVersion == version {
		return
	}
	tileSingles = singles
	tileDataPrim = dataPrim
	tileHostnamePrim = hostPrim
	tileSinglesVersion = version
}

func tileDataPrimitive() *tilePrimitive {
	ensureTileSingles()
	tileSinglesMu.RLock()
	defer tileSinglesMu.RUnlock()
	if tileDataPrim == nil {
		return &tilePrimitive{GrokName: "DATA", NameHint: "text", Regex: regexp.MustCompile(`^[\s\S]*$`)}
	}
	p := *tileDataPrim
	return &p
}

func tileHostnamePrimitive() *tilePrimitive {
	ensureTileSingles()
	tileSinglesMu.RLock()
	defer tileSinglesMu.RUnlock()
	if tileHostnamePrim == nil {
		return nil
	}
	p := *tileHostnamePrim
	return &p
}

// classifyToken returns the most-specific single-token primitive matching
// the whole token. NOTSPACE is the guaranteed fallback.
func classifyToken(tok string) *tilePrimitive {
	ensureTileSingles()
	tileSinglesMu.RLock()
	defer tileSinglesMu.RUnlock()
	for k := range tileSingles {
		if tileSingles[k].Regex.MatchString(tok) {
			p := tileSingles[k]
			return &p
		}
	}
	p := tileSingles[len(tileSingles)-1]
	return &p
}

// voteSingle returns the most-specific primitive that matches EVERY observed
// value — population voting, the cross-line generalisation of classifyToken.
func voteSingle(values []string) *tilePrimitive {
	ensureTileSingles()
	tileSinglesMu.RLock()
	defer tileSinglesMu.RUnlock()
	for k := range tileSingles {
		all := true
		for _, v := range values {
			if !tileSingles[k].Regex.MatchString(v) {
				all = false
				break
			}
		}
		if all {
			p := tileSingles[k]
			return &p
		}
	}
	p := tileSingles[len(tileSingles)-1]
	return &p
}

// ---------------------------------------------------------------------------
// Segmentation
// ---------------------------------------------------------------------------

// tileDelims are the byte delimiters that bound a token. They become literal
// segments and the tiler keeps tiling after them, so bracketed inner
// structure (a pid in app[123], a key=value pair) is recovered rather than
// swallowed. Intra-token punctuation (. - : / @ _ #) is deliberately NOT a
// delimiter, so dotted/hyphenated identifiers (host-1, com.example.Service,
// 192.168.1.1) stay whole; sub-token structure is recovered later by
// population-validated sub-tiling instead.
var tileDelims = [256]bool{}

func init() {
	for _, c := range []byte(" \t\"[]()=,|{}<>") {
		tileDelims[c] = true
	}
}

type tileUnitKind int

const (
	unitLiteral tileUnitKind = iota
	unitToken
	unitQuote
	unitComposite
)

// tileUnit is one structural unit of the template line.
type tileUnit struct {
	Kind tileUnitKind
	Text string // literal text / raw token / quoted inner text
	Comp *tileComposite
}

// segmentLine splits a template line into units. Quoted regions are only
// recognised at the top level (allowQuotes); inside one, a stray quote is
// just a delimiter.
func segmentLine(line string, allowQuotes bool) []tileUnit {
	var units []tileUnit
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			units = append(units, tileUnit{Kind: unitLiteral, Text: lit.String()})
			lit.Reset()
		}
	}
	i := 0
	for i < len(line) {
		c := line[i]
		if allowQuotes && c == '"' {
			if j := strings.IndexByte(line[i+1:], '"'); j >= 0 {
				flush()
				units = append(units, tileUnit{Kind: unitQuote, Text: line[i+1 : i+1+j]})
				i += j + 2
				continue
			}
		}
		if tileDelims[c] {
			lit.WriteByte(c)
			i++
			continue
		}
		// At a token boundary: try multi-token composite primitives first.
		if comp, n := matchComposite(line, i); n > 0 {
			flush()
			units = append(units, tileUnit{Kind: unitComposite, Text: line[i : i+n], Comp: comp})
			i += n
			continue
		}
		j := i
		for j < len(line) && !tileDelims[line[j]] {
			j++
		}
		flush()
		units = append(units, tileUnit{Kind: unitToken, Text: line[i:j]})
		i = j
	}
	flush()
	return units
}

// ---------------------------------------------------------------------------
// Population probe
// ---------------------------------------------------------------------------

// tileDistinctCap bounds the distinct values remembered per slot. Past this
// the slot is certainly variable; voting uses the capped set.
const tileDistinctCap = 64

type unitStats struct {
	matched  int
	distinct []string
	seen     map[string]bool
	overflow bool
}

func (s *unitStats) add(v string) {
	s.matched++
	if s.seen[v] {
		return
	}
	if len(s.distinct) >= tileDistinctCap {
		s.overflow = true
		return
	}
	s.seen[v] = true
	s.distinct = append(s.distinct, v)
}

// probeUnits aligns every sample line against the template's structure with
// a relaxed regex — literals verbatim, composites as their recogniser,
// tokens as (\S+), quoted regions as "([^"]*)" — and collects each slot's
// observed values. This is where the tiler stops guessing from one line and
// starts learning from the population.
func probeUnits(units []tileUnit, samples []string) ([]*unitStats, int) {
	var b strings.Builder
	b.WriteString(`^`)
	group := make([]int, len(units))
	g := 0
	for ui, u := range units {
		switch u.Kind {
		case unitLiteral:
			b.WriteString(regexp.QuoteMeta(u.Text))
		case unitComposite:
			b.WriteString(`(?:`)
			b.WriteString(u.Comp.Pattern)
			b.WriteString(`)`)
		case unitToken:
			g++
			group[ui] = g
			b.WriteString(`(\S+)`)
		case unitQuote:
			g++
			group[ui] = g
			b.WriteString(`"([^"]*)"`)
		}
	}
	b.WriteString(`\r?$`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, 0
	}
	stats := make([]*unitStats, len(units))
	for ui := range units {
		if group[ui] > 0 {
			stats[ui] = &unitStats{seen: map[string]bool{}}
		}
	}
	matchedLines := 0
	for _, line := range samples {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		matchedLines++
		for ui, gi := range group {
			if gi > 0 {
				stats[ui].add(m[gi])
			}
		}
	}
	return stats, matchedLines
}

// ---------------------------------------------------------------------------
// Typing (population voting) → render pieces
// ---------------------------------------------------------------------------

type pieceKind int

const (
	pieceLit pieceKind = iota
	pieceField
	pieceExpr
	pieceGroup
)

// tilePiece is one node of the render tree.
type tilePiece struct {
	Kind    pieceKind
	Text    string         // pieceLit: literal text; pieceExpr: grok expression
	Prim    *tilePrimitive // pieceField
	Sub     []tilePiece    // pieceGroup
	DashAlt bool           // pieceGroup rendered as (?:…|-)
}

func litPiece(text string) tilePiece        { return tilePiece{Kind: pieceLit, Text: text} }
func fieldPiece(p *tilePrimitive) tilePiece { return tilePiece{Kind: pieceField, Prim: p} }
func groupPiece(sub []tilePiece, dash bool) tilePiece {
	return tilePiece{Kind: pieceGroup, Sub: sub, DashAlt: dash}
}

// typeUnits converts structural units plus their observed value sets into a
// render tree.
func typeUnits(units []tileUnit, stats []*unitStats, depth int) []tilePiece {
	var pieces []tilePiece
	for ui, u := range units {
		var st *unitStats
		if stats != nil {
			st = stats[ui]
		}
		switch u.Kind {
		case unitLiteral:
			pieces = append(pieces, litPiece(u.Text))
		case unitComposite:
			pieces = append(pieces, tilePiece{Kind: pieceExpr, Text: u.Comp.Expr})
		case unitToken:
			pieces = append(pieces, typeToken(u.Text, st))
		case unitQuote:
			pieces = append(pieces, typeQuote(u.Text, st, depth))
		}
	}
	applySyslogHostGrammar(units, stats, pieces)
	return pieces
}

// applySyslogHostGrammar upgrades the token after a leading syslog timestamp
// to %{HOSTNAME:hostname}. In RFC3164 (`Oct  2 10:01:07 host-1 …`) and
// RFC5424 (`<134>1 2024-10-02T12:01:02Z host-1 …`) the slot after the
// timestamp is structurally the hostname, so it stays a typed field even when
// the sample only ever shows one host — the one case where a constant token
// is data, guaranteed by grammar rather than by variation.
func applySyslogHostGrammar(units []tileUnit, stats []*unitStats, pieces []tilePiece) {
	hostPrim := tileHostnamePrimitive()
	if hostPrim == nil {
		return
	}
	ci := -1
	for i, u := range units {
		if u.Kind != unitComposite {
			continue
		}
		if strings.Contains(u.Comp.Expr, "SYSLOGTIMESTAMP") && i == 0 {
			ci = i // RFC3164: line starts with the syslog timestamp
		} else if strings.Contains(u.Comp.Expr, "TIMESTAMP_ISO8601") {
			// RFC5424: `<pri>version ` precedes the timestamp.
			var prefix strings.Builder
			for _, p := range units[:i] {
				prefix.WriteString(p.Text)
			}
			if rfc5424PrefixRe.MatchString(prefix.String()) {
				ci = i
			}
		}
		break // only the line-leading timestamp qualifies
	}
	if ci < 0 || ci+2 >= len(units) {
		return
	}
	sep, host := units[ci+1], units[ci+2]
	if sep.Kind != unitLiteral || strings.TrimSpace(sep.Text) != "" || host.Kind != unitToken {
		return
	}
	// Never demote an already high-confidence typed field (e.g. LOGLEVEL).
	p := pieces[ci+2]
	if p.Kind == pieceField && p.Prim != nil && p.Prim.Promote {
		return
	}
	if p.Kind == pieceExpr || p.Kind == pieceGroup {
		return
	}
	// Every observed value must actually be hostname-shaped.
	if stats != nil && stats[ci+2] != nil && stats[ci+2].matched >= 2 {
		for _, v := range stats[ci+2].distinct {
			if !hostPrim.Regex.MatchString(v) {
				return
			}
		}
	} else if !hostPrim.Regex.MatchString(host.Text) {
		return
	}
	pieces[ci+2] = fieldPiece(hostPrim)
}

var rfc5424PrefixRe = regexp.MustCompile(`^<\d+>\d+\s*$`)

// typeToken types one token slot from its observed values.
func typeToken(tmpl string, st *unitStats) tilePiece {
	// Too little population evidence: classify the template token alone and
	// keep it a field (no demotion without evidence).
	if st == nil || st.matched < 2 {
		return fieldPiece(classifyToken(tmpl))
	}
	dashes := false
	var core []string
	for _, v := range st.distinct {
		if v == "-" {
			dashes = true
		} else {
			core = append(core, v)
		}
	}
	if len(core) == 0 {
		// Every observed value is "-": a placeholder literal.
		return litPiece("-")
	}
	prim := voteSingle(core)
	needDash := dashes && !prim.Regex.MatchString("-")
	if prim.Promote {
		return wrapDash([]tilePiece{fieldPiece(prim)}, needDash)
	}
	// Try sub-token structure shared by every observed value. This runs
	// BEFORE constant demotion: a constant token whose columns vote as
	// high-confidence types (0/0/1/2/3, HTTP/1.1, INFO/MainProcess) is
	// data that happens not to vary in the sample, not a keyword.
	if sub, ok := subSplitPieces(core); ok {
		return wrapDash(sub, dashes)
	}
	// A keyword: identical in every observed line.
	if !st.overflow && !dashes && len(core) == 1 {
		return litPiece(core[0])
	}
	return wrapDash([]tilePiece{fieldPiece(prim)}, needDash)
}

func wrapDash(pieces []tilePiece, dash bool) tilePiece {
	if !dash && len(pieces) == 1 {
		return pieces[0]
	}
	return groupPiece(pieces, dash)
}

// tileSubSeps are the intra-token separators the sub-tiler may split on.
// '.' and '-' are excluded on purpose: they appear inside atomic values
// (IPs, hostnames, UUIDs, versioned ids) far too often to split blindly.
var tileSubSeps = [256]bool{}

func init() {
	for _, c := range []byte(":/#@") {
		tileSubSeps[c] = true
	}
}

// splitSub splits a token into alternating parts and separator runs.
// sig encodes the full structure (separator runs verbatim, parts as P/0 for
// non-empty/empty) so two values are sub-aligned only when their structure
// is identical.
func splitSub(s string) (parts, seps []string, sig string, ok bool) {
	if s == "" {
		return nil, nil, "", false
	}
	var sigB strings.Builder
	i := 0
	for {
		j := i
		for j < len(s) && !tileSubSeps[s[j]] {
			j++
		}
		parts = append(parts, s[i:j])
		if i == j {
			sigB.WriteByte('0')
		} else {
			sigB.WriteByte('P')
		}
		if j >= len(s) {
			break
		}
		k := j
		for k < len(s) && tileSubSeps[s[k]] {
			k++
		}
		seps = append(seps, s[j:k])
		sigB.WriteString(s[j:k])
		i = k
		if k >= len(s) {
			parts = append(parts, "")
			sigB.WriteByte('0')
			break
		}
	}
	if len(seps) == 0 {
		return nil, nil, "", false
	}
	return parts, seps, sigB.String(), true
}

// subSplitPieces recovers intra-token structure: if every observed value
// shares the same separator signature (192.0.1.1:443 / 10.0.0.2:8080, or
// 0/0/1/2/3 / 1/0/2/2/4), each column is typed by population vote. Accepted
// only when at least one column carries a high-confidence (Promote) type —
// otherwise the split adds brittleness without information.
func subSplitPieces(core []string) ([]tilePiece, bool) {
	parts0, seps0, sig0, ok := splitSub(core[0])
	if !ok {
		return nil, false
	}
	cols := make([][]string, len(parts0))
	for _, v := range core {
		parts, _, sig, ok2 := splitSub(v)
		if !ok2 || sig != sig0 {
			return nil, false
		}
		for i, p := range parts {
			if p != "" {
				cols[i] = append(cols[i], p)
			}
		}
	}
	var pieces []tilePiece
	promoted := 0
	for i, p0 := range parts0 {
		if i > 0 {
			pieces = append(pieces, litPiece(seps0[i-1]))
		}
		if p0 == "" {
			continue
		}
		col := dedupeStrings(cols[i])
		prim := voteSingle(col)
		switch {
		case prim.Promote:
			promoted++
			pieces = append(pieces, fieldPiece(prim))
		case len(col) == 1:
			pieces = append(pieces, litPiece(col[0]))
		default:
			pieces = append(pieces, fieldPiece(prim))
		}
	}
	if promoted == 0 {
		return nil, false
	}
	return pieces, true
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// typeQuote types one quoted region. The inner text is tiled recursively
// against the region's observed inner values; the tiling is kept only when
// it explains (nearly) every observed value, otherwise the honest answer is
// %{DATA} — quoted strings are exactly where free text lives.
func typeQuote(inner string, st *unitStats, depth int) tilePiece {
	quoted := func(sub []tilePiece) tilePiece {
		out := make([]tilePiece, 0, len(sub)+2)
		out = append(out, litPiece(`"`))
		out = append(out, sub...)
		out = append(out, litPiece(`"`))
		return groupPiece(out, false)
	}
	if st == nil || st.matched < 2 {
		units := segmentLine(inner, false)
		return quoted(typeUnits(units, nil, depth+1))
	}
	if !st.overflow && len(st.distinct) == 1 {
		return quoted([]tilePiece{litPiece(st.distinct[0])})
	}
	if depth == 0 && inner != "" {
		if pieces, ok := tileInferPieces(inner, st.distinct, 1); ok {
			grok := renderPieces(pieces)
			if re, err := CompileGrok(grok, nil); err == nil &&
				matchesFraction(re, st.distinct, 0.95) {
				if p, _ := pieceCounts(pieces); p > 0 {
					return quoted(pieces)
				}
			}
		}
	}
	return quoted([]tilePiece{fieldPiece(tileDataPrimitive())})
}

func matchesFraction(re *regexp.Regexp, values []string, frac float64) bool {
	if len(values) == 0 {
		return false
	}
	hits := 0
	for _, v := range values {
		if re.MatchString(v) {
			hits++
		}
	}
	return float64(hits) >= frac*float64(len(values))
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// renderPieces turns a render tree into a Grok string. Field names are
// derived from the literal key preceding the field (`key=` / `key:`), else
// from the primitive's hint, deduped with _2/_3 suffixes.
func renderPieces(pieces []tilePiece) string {
	used := make(map[string]int)
	var b strings.Builder
	// pending accumulates the literal text immediately preceding the next
	// field, so a key in `id=%{INT}` names the field even when intervening
	// segments were demoted to literals.
	var pending strings.Builder
	var walk func(ps []tilePiece)
	walk = func(ps []tilePiece) {
		for _, p := range ps {
			switch p.Kind {
			case pieceLit:
				b.WriteString(regexp.QuoteMeta(p.Text))
				pending.WriteString(p.Text)
			case pieceExpr:
				b.WriteString(p.Text)
				pending.Reset()
			case pieceField:
				base := tileFieldName(pending.String(), p.Prim)
				pending.Reset()
				n := used[base]
				used[base] = n + 1
				name := base
				if n > 0 {
					name = fmt.Sprintf("%s_%d", base, n+1)
				}
				fmt.Fprintf(&b, "%%{%s:%s}", p.Prim.GrokName, name)
			case pieceGroup:
				if p.DashAlt {
					b.WriteString("(?:")
				}
				walk(p.Sub)
				if p.DashAlt {
					b.WriteString("|-)")
				}
			}
		}
	}
	walk(pieces)
	return b.String()
}

// tileFieldName names a field from the literal text preceding it, but only
// when that literal ends in a real key separator (`key=` or `key:`) — a
// genuine key/value signal. Otherwise it returns the primitive's hint. This
// avoids naming a pid field after the program token that happens to precede
// it (e.g. `app1[%{INT}]`), which carries no key/value relationship.
func tileFieldName(precedingLiteral string, prim *tilePrimitive) string {
	trimmed := strings.TrimRight(precedingLiteral, " \t")
	if !strings.HasSuffix(trimmed, "=") && !strings.HasSuffix(trimmed, ":") {
		return prim.NameHint
	}
	key := strings.TrimRight(trimmed, ":=")
	parts := strings.FieldsFunc(key, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '[' || r == ']' || r == '(' || r == ')' || r == '"' || r == ',' || r == '|'
	})
	if len(parts) > 0 {
		cand := canonicalName(strings.ToLower(parts[len(parts)-1]))
		if isValidName(cand) && !weakFieldName(cand) {
			return cand
		}
	}
	return prim.NameHint
}

// pieceCounts walks the render tree and counts high-confidence (promoted)
// fields and speculative (non-promote) fields. Composite expressions count
// as promoted — they are timestamps.
func pieceCounts(pieces []tilePiece) (promoted, litFields int) {
	for _, p := range pieces {
		switch p.Kind {
		case pieceField:
			if p.Prim.Promote {
				promoted++
			} else {
				litFields++
			}
		case pieceExpr:
			promoted++
		case pieceGroup:
			sp, sl := pieceCounts(p.Sub)
			promoted += sp
			litFields += sl
		}
	}
	return promoted, litFields
}

// ---------------------------------------------------------------------------
// Top level: brute force over template lines, score, relax
// ---------------------------------------------------------------------------

// tileInferPieces runs segment → probe → type for one template line against
// a sample population. Used at the top level (samples = whole lines) and
// recursively for quoted regions (samples = the region's observed values).
func tileInferPieces(template string, samples []string, depth int) ([]tilePiece, bool) {
	units := segmentLine(template, depth == 0)
	hasSlot := false
	for _, u := range units {
		if u.Kind != unitLiteral {
			hasSlot = true
			break
		}
	}
	if !hasSlot {
		return nil, false
	}
	stats, _ := probeUnits(units, samples)
	return typeUnits(units, stats, depth), true
}

// tileCandidate is a fully-built tiling scored on the eval set.
type tileCandidate struct {
	Grok      string
	Matched   int
	Total     int
	Typed     int
	Promoted  int // count of high-confidence (Promote) fields kept
	LitFields int // non-promote fields kept as fields (precision cost)
	pieces    []tilePiece
}

// buildTiling tiles one template line against the sample population and
// scores the result against eval. Returns nil if it produces no usable grok.
func buildTiling(template string, sample, eval []string, ctl *scanCtl) *tileCandidate {
	pieces, ok := tileInferPieces(template, sample, 0)
	if !ok {
		return nil
	}
	grok := renderPieces(pieces)
	re, err := CompileGrok(grok, nil)
	if err != nil {
		return nil
	}
	if !re.MatchString(template) {
		return nil
	}
	matched := evaluateCoverageCtl(re, eval, ctl)
	if matched == 0 {
		return nil
	}
	cand := &tileCandidate{
		Grok:    grok,
		Matched: matched,
		Total:   len(eval),
		Typed:   typedCaptureCount(grok),
		pieces:  pieces,
	}
	cand.Promoted, cand.LitFields = pieceCounts(pieces)
	return cand
}

// tileShapeTemplates caps how many distinct line shapes are tiled per call.
const tileShapeTemplates = 16

// tileShapes brute-forces the tiler over a set of distinct template lines —
// one tiling per line shape. Template candidates are deduped by their
// char-class skeleton so genuinely different shapes are tried, capped for
// cost. This is the tiler's clustering: each shape's relaxed probe only
// matches the lines that share its literal skeleton, so each candidate is
// typed by exactly its own population.
func tileShapes(sample, eval []string, maxTemplates int, ctl *scanCtl) []*tileCandidate {
	templates := pickTemplateLines(sample, maxTemplates)
	var out []*tileCandidate
	for _, t := range templates {
		if c := buildTiling(t, sample, eval, ctl); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// bestTiling picks the strongest shape, relaxing its tail when that buys
// real coverage.
func bestTiling(shapes []*tileCandidate, eval []string, ctl *scanCtl) *tileCandidate {
	var best *tileCandidate
	for _, c := range shapes {
		if betterTiling(c, best) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	if best.Matched < len(eval) {
		if relaxed := relaxTail(best, eval, ctl); relaxed != nil {
			best = relaxed
		}
	}
	return best
}

// relaxTailMaxCuts bounds how many tail cut points are evaluated against the
// eval set; each costs one full coverage scan.
const relaxTailMaxCuts = 24

// relaxTail tries replacing the tail of the best tiling with
// %{GREEDYDATA:message} at successive piece boundaries. Logs whose prefix is
// rigid (timestamp, level, source) but whose message tail varies line-to-line
// fail the strict tiling; the relaxed form is what an expert writes. Cuts
// after literals are tried too, so a constant separator stays out of the
// message field (`\| %{GREEDYDATA:message}` rather than the message starting
// with " | "); on equal coverage the longest prefix wins. The relaxation
// must buy a real coverage gain (≥2% of eval, min 1 line) so a uniform
// input keeps its fully-typed pattern.
func relaxTail(best *tileCandidate, eval []string, ctl *scanCtl) *tileCandidate {
	margin := max(len(eval)/50, 1)
	var bestVar *tileCandidate
	promoted := 0
	cuts := 0
	for i := 0; i < len(best.pieces)-1 && cuts < relaxTailMaxCuts; i++ {
		p, _ := pieceCounts(best.pieces[i : i+1])
		promoted += p
		if promoted == 0 {
			continue
		}
		// Cut only at real boundaries: right after a field/group, or at the
		// end of the literal run that precedes the next field. Interior
		// literal pieces (a demoted keyword followed by its delimiter run)
		// are not distinct cut points.
		if best.pieces[i].Kind == pieceLit && best.pieces[i+1].Kind == pieceLit {
			continue
		}
		cuts++
		prefix := best.pieces[:i+1]
		grok := renderPieces(prefix) + `%{GREEDYDATA:message}`
		re, err := CompileGrok(grok, nil)
		if err != nil {
			continue
		}
		// Floor-prune: a cut that cannot strictly beat best.Matched+margin
		// bails out mid-scan instead of completing a full coverage pass.
		// Accepted candidates always carry exact counts (pruning only fires
		// when the true count is <= floor; see coverage.go).
		matched := evaluateCoverageWithFloorCtl(re, eval, best.Matched+margin-1, ctl)
		if matched-best.Matched < margin {
			continue
		}
		cand := &tileCandidate{
			Grok:    grok,
			Matched: matched,
			Total:   len(eval),
			Typed:   typedCaptureCount(grok),
			pieces:  prefix,
		}
		cand.Promoted, cand.LitFields = pieceCounts(prefix)
		// Replace unless the incumbent is strictly better: on a full tie the
		// later (longer-prefix) cut wins, keeping constant separators out of
		// the message field.
		if bestVar == nil || !betterTiling(bestVar, cand) {
			bestVar = cand
		}
	}
	return bestVar
}

// betterTiling: more eval matches first, then more high-confidence fields,
// then fewer speculative (non-promote) fields, then more typed captures.
func betterTiling(a, b *tileCandidate) bool {
	if b == nil {
		return true
	}
	if a.Matched != b.Matched {
		return a.Matched > b.Matched
	}
	if a.Promoted != b.Promoted {
		return a.Promoted > b.Promoted
	}
	if a.LitFields != b.LitFields {
		return a.LitFields < b.LitFields
	}
	return a.Typed > b.Typed
}

var tileSkeletonRe = regexp.MustCompile(`[A-Za-z]+|\d+|\s+|[^A-Za-z\d\s]`)

// pickTemplateLines returns up to max distinct sample lines, deduped by a
// coarse char-class skeleton so we brute-force over genuinely different
// shapes rather than near-duplicates. Longer (more-field) lines come first.
func pickTemplateLines(sample []string, max int) []string {
	seen := make(map[string]bool)
	var out []string
	idx := make([]int, len(sample))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return len(sample[idx[a]]) > len(sample[idx[b]]) })
	for _, i := range idx {
		line := sample[i]
		skel := skeletonOf(line)
		if seen[skel] {
			continue
		}
		seen[skel] = true
		out = append(out, line)
		if len(out) >= max {
			break
		}
	}
	return out
}

func skeletonOf(line string) string {
	var b strings.Builder
	for _, m := range tileSkeletonRe.FindAllString(line, -1) {
		switch {
		case m[0] >= '0' && m[0] <= '9':
			b.WriteByte('9')
		case (m[0] >= 'a' && m[0] <= 'z') || (m[0] >= 'A' && m[0] <= 'Z'):
			b.WriteByte('A')
		case m[0] == ' ' || m[0] == '\t':
			b.WriteByte('_')
		default:
			b.WriteByte(m[0])
		}
	}
	return b.String()
}

// Honesty gates for the tiler. It can fit a 100%-coverage pattern to almost
// any single line, so without these it would overfit tiny or heterogeneous
// inputs and pre-empt the deliberate fallback behaviour.
var (
	// tilingMinLines is the absolute evidence floor: below this many lines
	// any tiled pattern is an accident, not a format.
	tilingMinLines = 3
	// tilingShortSampleMinLines marks the short-sample regime. Below it a
	// tiling must clear tilingShortSampleCoverage AND carry at least one
	// shared literal keyword — proof the lines share actual content, not
	// just token-count shape (three random four-word sentences align
	// structurally; three "worker <w> processed <n> jobs" lines share
	// vocabulary).
	tilingShortSampleMinLines = 10
	tilingShortSampleCoverage = 0.85
	// tilingMinCoverage is the share of the input a tiling must explain to
	// be trustworthy. Heterogeneous inputs (every line a different shape)
	// produce a best tiling that covers only its own cluster — well below
	// this floor — and correctly fall through.
	tilingMinCoverage = 0.50
	// tilingMinPromoted requires at least one unambiguous high-confidence
	// field (timestamp, IP, number, level, id…). A tiling that is all
	// literals plus a lone NOTSPACE carries no more information than the
	// GREEDYDATA fallback, so it should not win.
	tilingMinPromoted = 1
)

// Union tuning: when the best single shape explains too little of the input
// but a handful of shapes together explain (nearly) all of it, the input is
// a small multi-format stream and a branch alternation is the right single
// pattern. Mirrors the thresholds the retired drain engine used.
var (
	// tiledUnionTrigger is the best-single-shape coverage below which a
	// union is attempted.
	tiledUnionTrigger = 0.85
	// tiledUnionMinBranchLines rejects one-off shapes from unions. A branch
	// needs at least two newly-explained lines to count as a reusable shape.
	tiledUnionMinBranchLines = 2
	// tiledUnionMaxBranches bounds the union to "a small number of formats".
	tiledUnionMaxBranches = 10
	// tiledUnionMinCombined is the floor the unioned coverage must clear.
	tiledUnionMinCombined = 0.90
	// tiledUnionMinGain is the absolute coverage improvement the union must
	// deliver over the best single shape to be worth returning.
	tiledUnionMinGain = 0.15
)

// literalKeywords counts literal pieces that carry actual content (at least
// one letter or digit) — shared vocabulary between lines, as opposed to
// delimiter runs. This is the short-sample honesty signal.
func literalKeywords(pieces []tilePiece) int {
	n := 0
	for _, p := range pieces {
		switch p.Kind {
		case pieceLit:
			if strings.ContainsFunc(p.Text, func(r rune) bool {
				return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			}) {
				n++
			}
		case pieceGroup:
			n += literalKeywords(p.Sub)
		}
	}
	return n
}

// tiledUnion alternates complementary shapes into one pattern when no single
// shape explains the input. Shapes are taken in descending matched order;
// each must newly explain at least tiledUnionMinBranchLines lines and carry
// a high-confidence field.
func tiledUnion(shapes []*tileCandidate, eval []string, bestCov float64, diag io.Writer, ctl *scanCtl) *DiscoveredPattern {
	if len(shapes) < 2 {
		return nil
	}
	ordered := make([]*tileCandidate, len(shapes))
	copy(ordered, shapes)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].Matched > ordered[b].Matched })

	remaining := make([]bool, len(eval))
	for i := range remaining {
		remaining[i] = true
	}
	var branches []string
	for _, c := range ordered {
		if len(branches) >= tiledUnionMaxBranches {
			break
		}
		if c.Promoted < tilingMinPromoted {
			continue
		}
		re, err := CompileGrok(c.Grok, nil)
		if err != nil {
			continue
		}
		gained := 0
		var gainedIdx []int
		for i, line := range eval {
			if remaining[i] && re.MatchString(line) {
				gained++
				gainedIdx = append(gainedIdx, i)
			}
		}
		if gained < tiledUnionMinBranchLines {
			continue
		}
		for _, i := range gainedIdx {
			remaining[i] = false
		}
		branches = append(branches, c.Grok)
	}
	if len(branches) < 2 {
		return nil
	}
	parts := make([]string, 0, len(branches))
	for _, b := range branches {
		parts = append(parts, "(?:"+b+")")
	}
	unionGrok := "(?:" + strings.Join(parts, "|") + ")"
	re, err := CompileGrok(unionGrok, nil)
	if err != nil {
		fmt.Fprintf(diag, "tiling union: failed to compile: %v\n", err)
		return nil
	}
	matched := evaluateCoverageCtl(re, eval, ctl)
	cov := ratio(matched, len(eval))
	fmt.Fprintf(diag, "tiling union: branches=%d matched=%d/%d coverage=%.3f (best single=%.3f)\n",
		len(branches), matched, len(eval), cov, bestCov)
	if cov < tiledUnionMinCombined || cov-bestCov < tiledUnionMinGain {
		return nil
	}
	return &DiscoveredPattern{
		Source:       fmt.Sprintf("inferred:multi(%d)", len(branches)),
		SourceFamily: "inferred",
		Grok:         unionGrok,
		Coverage:     cov,
		MatchedCount: matched,
		TotalLines:   len(eval),
	}
}

// tryTiling is the stage entry point, mirroring tryStructured/tryTextEnvelope.
func tryTiling(sample, all []string, diag io.Writer, ctl *scanCtl) *DiscoveredPattern {
	if len(all) < tilingMinLines {
		fmt.Fprintf(diag, "tiling: skipped, %d lines < %d evidence floor\n", len(all), tilingMinLines)
		return nil
	}
	shapes := tileShapes(sample, all, tileShapeTemplates, ctl)
	cand := bestTiling(shapes, all, ctl)
	if cand == nil {
		return nil
	}
	cov := ratio(cand.Matched, cand.Total)
	// No single shape explains the input: try a union of complementary
	// shapes before giving up (small multi-format streams).
	if cov < tiledUnionTrigger {
		if u := tiledUnion(shapes, all, cov, diag, ctl); u != nil {
			return u
		}
	}
	if len(all) < tilingShortSampleMinLines &&
		(cov < tilingShortSampleCoverage || literalKeywords(cand.pieces) == 0) {
		fmt.Fprintf(diag, "tiling: skipped weak short-sample candidate (coverage=%.3f keywords=%d, lines=%d < %d)\n",
			cov, literalKeywords(cand.pieces), len(all), tilingShortSampleMinLines)
		return nil
	}
	// A tiling is informative when it carries a high-confidence field, or —
	// for keyword-anchored formats whose data slots are all free text —
	// when shared vocabulary pins down at least two typed captures. A lone
	// NOTSPACE with no anchor is no better than the GREEDYDATA fallback.
	informative := cand.Promoted >= tilingMinPromoted ||
		(literalKeywords(cand.pieces) > 0 && cand.Typed >= 2)
	if cov < tilingMinCoverage || !informative {
		fmt.Fprintf(diag, "tiling: skipped weak candidate (coverage=%.3f promoted=%d typed=%d)\n",
			cov, cand.Promoted, cand.Typed)
		return nil
	}
	fmt.Fprintf(diag, "tiling: matched=%d/%d typed=%d promoted=%d litfields=%d\n",
		cand.Matched, cand.Total, cand.Typed, cand.Promoted, cand.LitFields)
	return &DiscoveredPattern{
		Source:       "inferred:Tiled",
		SourceFamily: "inferred",
		Grok:         cand.Grok,
		Coverage:     cov,
		MatchedCount: cand.Matched,
		TotalLines:   cand.Total,
	}
}
