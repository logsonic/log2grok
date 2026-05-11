package log2grok

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strings"
)

// MultilineMode controls how continuation lines are identified.
//
//   - MultilineHeader: a fresh record starts whenever a line matches the
//     configured Header regex; everything until the next header line is
//     a continuation. This is the right setting for "every record starts
//     with a timestamp" formats (Java, Python, slf4j, MySQL slow query).
//   - MultilineIndent: continuation lines are those that begin with a
//     space or tab. This is the most common Unix convention (rsyslog,
//     systemd-journal-style multi-line entries).
//   - MultilineCustom: callers supply their own IsContinuation
//     predicate. Useful for ad-hoc formats.
type MultilineMode int

const (
	MultilineHeader MultilineMode = iota
	MultilineIndent
	MultilineCustom
)

// MultilineConfig describes how to fold a raw stream of \n-delimited
// lines into one record per `Record`. The zero value is *not* a
// runnable config — set Mode and the matching field (Header or
// IsContinuation) before use.
//
// Joiner is the string inserted between the header and each
// continuation. Default is " " (a single space) so the folded record
// is itself a valid single-line input for Decoder/Discover (whose
// %{GREEDYDATA} primitive does not span newlines). Callers that want
// to preserve original formatting can override to "\n".
//
// MaxLines caps the number of continuation lines folded into a single
// record (default 100). MaxBytes caps the total byte length of a
// folded record (default 1 MiB). When either limit is hit the record
// is emitted as-is and the next physical line starts a new record.
// This protects against runaway stack traces blowing through memory.
type MultilineConfig struct {
	Mode           MultilineMode
	Header         *regexp.Regexp
	IsContinuation func(line string) bool
	Joiner         string
	MaxLines       int
	MaxBytes       int
}

// ErrMultilineConfig is returned by JoinMultiline / NewMultilineJoiner
// when the configuration is incomplete (e.g. Mode=MultilineHeader but
// Header is nil).
var ErrMultilineConfig = errors.New("log2grok: invalid MultilineConfig")

// validate normalises the config and reports configuration errors. We
// fail fast rather than silently treating "all lines as headers" so
// the caller knows immediately when their config is wrong.
func (c *MultilineConfig) validate() error {
	switch c.Mode {
	case MultilineHeader:
		if c.Header == nil {
			return ErrMultilineConfig
		}
	case MultilineIndent:
		// fine
	case MultilineCustom:
		if c.IsContinuation == nil {
			return ErrMultilineConfig
		}
	default:
		return ErrMultilineConfig
	}
	if c.Joiner == "" {
		c.Joiner = " "
	}
	if c.MaxLines <= 0 {
		c.MaxLines = 100
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 1 << 20
	}
	return nil
}

// isContinuation answers whether `line` is a continuation under the
// configured mode. For MultilineHeader, a continuation is "anything
// that does NOT look like a header". For MultilineIndent, a
// continuation starts with whitespace.
func (c *MultilineConfig) isContinuation(line string) bool {
	switch c.Mode {
	case MultilineHeader:
		return !c.Header.MatchString(line)
	case MultilineIndent:
		if len(line) == 0 {
			return false
		}
		return line[0] == ' ' || line[0] == '\t'
	case MultilineCustom:
		return c.IsContinuation(line)
	}
	return false
}

// JoinMultiline reads \n-delimited lines from r and folds them into
// multi-line records per cfg. The returned slice contains one entry
// per logical record, in input order, with continuation lines joined
// by cfg.Joiner.
//
// Blank lines are preserved unless they are continuations of an open
// record (in which case they're joined). Records exceeding MaxLines or
// MaxBytes are emitted truncated; truncation has no special marker.
func JoinMultiline(r io.Reader, cfg MultilineConfig) ([]string, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("log2grok: nil reader")
	}
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1<<20), 8<<20)
	var (
		out  []string
		open strings.Builder
		// nLines counts continuation lines added to the current open
		// record (the header itself is not counted, so MaxLines=N caps
		// the body at N continuations).
		nLines int
	)
	flush := func() {
		if open.Len() == 0 {
			return
		}
		out = append(out, open.String())
		open.Reset()
		nLines = 0
	}
	for s.Scan() {
		line := s.Text()
		if open.Len() == 0 {
			open.WriteString(line)
			continue
		}
		if cfg.isContinuation(line) {
			if nLines >= cfg.MaxLines || open.Len()+len(line)+len(cfg.Joiner) > cfg.MaxBytes {
				flush()
				open.WriteString(line)
				continue
			}
			open.WriteString(cfg.Joiner)
			open.WriteString(line)
			nLines++
			continue
		}
		flush()
		open.WriteString(line)
	}
	flush()
	return out, s.Err()
}

// JoinMultilineStrings is the all-in-memory variant of JoinMultiline.
// Useful when callers already have a []string (e.g. from a /parse HTTP
// request body) and don't want to wrap it in a bytes.Reader.
func JoinMultilineStrings(lines []string, cfg MultilineConfig) ([]string, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	var (
		out    []string
		open   strings.Builder
		nLines int
	)
	flush := func() {
		if open.Len() == 0 {
			return
		}
		out = append(out, open.String())
		open.Reset()
		nLines = 0
	}
	for _, line := range lines {
		if open.Len() == 0 {
			open.WriteString(line)
			continue
		}
		if cfg.isContinuation(line) {
			if nLines >= cfg.MaxLines || open.Len()+len(line)+len(cfg.Joiner) > cfg.MaxBytes {
				flush()
				open.WriteString(line)
				continue
			}
			open.WriteString(cfg.Joiner)
			open.WriteString(line)
			nLines++
			continue
		}
		flush()
		open.WriteString(line)
	}
	flush()
	return out, nil
}

// CommonMultilineConfigs returns ready-made configs for the most common
// multi-line shapes. Use as a starting point; clone and override fields
// to taste.
//
//   - "iso8601": header is any line beginning with an ISO 8601 date
//     (yyyy-mm-dd). Covers Spring Boot, slf4j, Python's default
//     formatter, journalctl with --output=short-iso, etc.
//   - "syslog": header is a classic syslog timestamp ("Jan  2 15:04:05").
//   - "java-stack": header is anything NOT starting with `at `,
//     `Caused by`, `\tat`, or a tab. The corresponding mode is Custom
//     because Java continuation lines have several recognised prefixes.
func CommonMultilineConfigs() map[string]MultilineConfig {
	javaCont := regexp.MustCompile(`^(?:\s|at\s|Caused by|\.{3} )`)
	return map[string]MultilineConfig{
		"iso8601": {
			Mode:   MultilineHeader,
			Header: regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`),
		},
		"syslog": {
			Mode:   MultilineHeader,
			Header: regexp.MustCompile(`^[A-Z][a-z]{2}\s{1,2}\d{1,2}\s\d{2}:\d{2}:\d{2}`),
		},
		"java-stack": {
			Mode:           MultilineCustom,
			IsContinuation: func(line string) bool { return javaCont.MatchString(line) },
		},
	}
}
