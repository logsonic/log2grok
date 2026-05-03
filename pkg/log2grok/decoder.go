package log2grok

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/logsonic/log2grok/internal/pattern"
)

// PatternSpec describes a single Grok rule. CustomPatterns is a per-spec
// extension to the global primitives table (primitives.json) — entries
// here override globals during expansion. Priority is reserved for
// future multi-pattern decoders; the current single-pattern Decoder
// ignores it but accepts it so callers can continue to express
// preferred ordering.
type PatternSpec struct {
	Name           string
	Grok           string
	CustomPatterns map[string]string
	Priority       int
}

// DecoderOptions tunes Decoder behavior. Zero value yields a strict
// regex decoder with no enrichment.
type DecoderOptions struct {
	// SmartDecode adds auxiliary capture entries (_ipv4_addr,
	// _email_addr, _urls, _mac_addr, _uuids) for each matched line.
	// They are exposed via LineResult.Smart, never merged into Fields,
	// so callers can keep them in a separate namespace.
	SmartDecode bool
}

// LineResult is the outcome of decoding one input line. On a successful
// match Fields is the named-capture map (always non-nil so range loops
// don't need to check for nil) and Error is empty. On a miss Matched is
// false, Fields is nil, and Error carries a short human-readable reason.
type LineResult struct {
	Raw     string
	Matched bool
	Pattern string
	Fields  map[string]string
	Smart   map[string]string
	Error   string
}

// Decoder is a compiled, reusable parser for one PatternSpec. It is safe
// for concurrent use after construction; the underlying *regexp.Regexp
// is goroutine-safe per the standard library contract.
type Decoder struct {
	spec PatternSpec
	opts DecoderOptions
	re   *regexp.Regexp
	// names is the cached SubexpNames slice. Storing it once avoids the
	// per-line allocation that re.SubexpNames() would otherwise incur
	// (the standard library returns a fresh slice on every call).
	names []string
}

// ErrEmptyPattern is returned by NewDecoder when spec.Grok is blank.
var ErrEmptyPattern = errors.New("log2grok: pattern is empty")

// NewDecoder validates the spec, expands its Grok via the active
// primitives table (plus any per-spec CustomPatterns), and compiles the
// result into an anchored Go regular expression. The compiled Decoder
// can then be reused across many Decode/DecodeReader invocations.
func NewDecoder(spec PatternSpec, opts DecoderOptions) (*Decoder, error) {
	if spec.Grok == "" {
		return nil, ErrEmptyPattern
	}
	re, err := pattern.CompileGrok(spec.Grok, spec.CustomPatterns)
	if err != nil {
		return nil, fmt.Errorf("log2grok: compile %q: %w", spec.Name, err)
	}
	return &Decoder{
		spec:  spec,
		opts:  opts,
		re:    re,
		names: re.SubexpNames(),
	}, nil
}

// Pattern returns the spec the decoder was built from. The returned
// value is a copy of the original spec — mutating it has no effect on
// the decoder.
func (d *Decoder) Pattern() PatternSpec {
	return d.spec
}

// Decode runs the compiled pattern across every line and returns one
// LineResult per input. The returned slice has the same length as
// lines, in the same order, so callers can correlate by index.
func (d *Decoder) Decode(lines []string) []LineResult {
	out := make([]LineResult, len(lines))
	for i, line := range lines {
		out[i] = d.decodeOne(line)
	}
	return out
}

// DecodeReader streams lines from r through the decoder, invoking cb for
// each result. Returns the running totals of matched and failed lines
// plus any IO error encountered while reading. A non-nil error returned
// by cb terminates the stream early and is propagated to the caller.
//
// The scanner uses a 1 MiB initial buffer and an 8 MiB maximum so very
// long log lines (e.g. stack traces, JSON blobs) are handled without
// the default bufio.Scanner truncation.
func (d *Decoder) DecodeReader(r io.Reader, cb func(LineResult) error) (matched, failed int, err error) {
	if r == nil {
		return 0, 0, errors.New("log2grok: nil reader")
	}
	if cb == nil {
		return 0, 0, errors.New("log2grok: nil callback")
	}
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for s.Scan() {
		res := d.decodeOne(s.Text())
		if res.Matched {
			matched++
		} else {
			failed++
		}
		if cberr := cb(res); cberr != nil {
			return matched, failed, cberr
		}
	}
	return matched, failed, s.Err()
}

// decodeOne is the per-line worker shared by Decode and DecodeReader.
// It deliberately keeps the result struct small: zero-allocation on a
// miss (Fields/Smart left nil) and one allocation per matched field on
// a hit. A duplicate-name capture (Go regexp permits these via the
// dedup logic in CompileGrok) keeps the last value seen, which matches
// elastic/go-grok's behavior and the convention used by the previous
// in-house tokenizer.
func (d *Decoder) decodeOne(line string) LineResult {
	res := LineResult{Raw: line, Pattern: d.spec.Name}
	matches := d.re.FindStringSubmatch(line)
	if matches == nil {
		res.Error = "log line did not match pattern"
		if d.spec.Name != "" {
			res.Error = fmt.Sprintf("log line did not match the %q pattern", d.spec.Name)
		}
		return res
	}

	res.Matched = true
	// Skip index 0 (the whole-match) and any unnamed groups — those are
	// expansion scaffolding, never user-meaningful captures.
	fields := make(map[string]string, len(d.names))
	for i, name := range d.names {
		if i == 0 || name == "" {
			continue
		}
		fields[name] = matches[i]
	}
	res.Fields = fields

	if d.opts.SmartDecode {
		if sm := smartDecode(line); sm != nil {
			res.Smart = sm
		}
	}
	return res
}
