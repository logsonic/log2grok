package log2grok

import (
	"errors"
	"testing"
	"time"
)

func TestInferTimestampHint_HTTPDATE(t *testing.T) {
	h := inferTimestampHint(`%{IP:ip} %{USER:u} \[%{HTTPDATE:timestamp}\] %{GREEDYDATA:msg}`)
	if h.Field != "timestamp" {
		t.Errorf("Field: %q", h.Field)
	}
	if h.Source != "HTTPDATE" {
		t.Errorf("Source: %q", h.Source)
	}
	if h.Layout == "" {
		t.Error("Layout empty")
	}
}

func TestInferTimestampHint_ISO8601(t *testing.T) {
	h := inferTimestampHint(`%{TIMESTAMP_ISO8601:ts} %{GREEDYDATA:m}`)
	if h.Field != "ts" || h.Source != "TIMESTAMP_ISO8601" {
		t.Errorf("hint: %+v", h)
	}
}

func TestInferTimestampHint_SyslogMultiField(t *testing.T) {
	h := inferTimestampHint(`%{SYSLOGTIMESTAMP:syslog_ts} %{GREEDYDATA:m}`)
	if h.Source != "SYSLOGTIMESTAMP" {
		t.Errorf("Source: %q (full hint: %+v)", h.Source, h)
	}
}

func TestInferTimestampHint_EmptyOnUnknown(t *testing.T) {
	h := inferTimestampHint(`%{IP:ip} %{GREEDYDATA:m}`)
	if !h.IsZero() {
		t.Errorf("expected zero hint, got %+v", h)
	}
}

func TestParseTimestamp_ISO8601(t *testing.T) {
	hint := TimestampHint{Field: "ts", Layout: "2006-01-02T15:04:05.000Z07:00"}
	ts, err := ParseTimestamp(map[string]string{"ts": "2026-03-15T10:20:30.500Z"}, hint)
	if err != nil {
		t.Fatalf("ParseTimestamp: %v", err)
	}
	if ts.Year() != 2026 || ts.Month() != 3 || ts.Day() != 15 {
		t.Errorf("got %v", ts)
	}
}

func TestParseTimestamp_NoHintReturnsError(t *testing.T) {
	_, err := ParseTimestamp(map[string]string{"ts": "2026-03-15T10:20:30Z"}, TimestampHint{})
	if !errors.Is(err, ErrNoTimestamp) {
		t.Errorf("want ErrNoTimestamp, got %v", err)
	}
}

func TestParseTimestamp_MissingField(t *testing.T) {
	hint := TimestampHint{Field: "ts", Layout: "2006-01-02T15:04:05.000Z07:00"}
	_, err := ParseTimestamp(map[string]string{"other": "x"}, hint)
	if !errors.Is(err, ErrNoTimestamp) {
		t.Errorf("want ErrNoTimestamp, got %v", err)
	}
}

func TestDiscover_PopulatesTimestampHint(t *testing.T) {
	lines := []string{
		`192.168.1.1 - - [23/Jan/2026:14:05:01 +0000] "GET / HTTP/1.1" 200 1`,
		`10.0.0.1 - - [23/Jan/2026:14:05:02 +0000] "GET /a HTTP/1.1" 200 2`,
		`10.0.0.2 - - [23/Jan/2026:14:05:03 +0000] "GET /b HTTP/1.1" 200 3`,
	}
	dp, err := Discover(lines, Options{LibraryThreshold: 0.75})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if dp.TimestampHint.Source == "" {
		t.Errorf("Discover should populate TimestampHint, got %+v", dp.TimestampHint)
	}
}

func TestDecoder_TimestampConvenienceMethod(t *testing.T) {
	dec, err := NewDecoder(PatternSpec{
		Grok: `%{TIMESTAMP_ISO8601:ts} %{LOGLEVEL:lvl} %{GREEDYDATA:msg}`,
	}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	r := dec.Decode([]string{`2026-03-15T10:20:30.500Z INFO hello world`})[0]
	if !r.Matched {
		t.Fatalf("expected match")
	}
	if dec.TimestampHint().Field != "ts" {
		t.Errorf("hint.Field: %q", dec.TimestampHint().Field)
	}
	ts, err := dec.Timestamp(r)
	if err != nil {
		t.Fatalf("Timestamp: %v", err)
	}
	if ts.Year() != 2026 {
		t.Errorf("year: %d", ts.Year())
	}
}

func TestParseTimestamp_CommaFractional(t *testing.T) {
	hint := TimestampHint{Field: "ts", Layout: "2006-01-02 15:04:05.000"}
	// log4j-style comma fractional separator
	if _, err := ParseTimestamp(map[string]string{"ts": "2026-03-15 10:20:30,500"}, hint); err != nil {
		t.Errorf("ParseTimestamp comma fractional: %v", err)
	}
}

func TestDecoder_TimestampReturnsErrorOnMiss(t *testing.T) {
	dec, _ := NewDecoder(PatternSpec{Grok: `%{TIMESTAMP_ISO8601:ts}`}, DecoderOptions{})
	r := dec.Decode([]string{"definitely not a timestamp"})[0]
	if _, err := dec.Timestamp(r); !errors.Is(err, ErrNoTimestamp) {
		t.Errorf("want ErrNoTimestamp on unmatched, got %v", err)
	}
}

func _useTime(_ time.Time) {} // silence unused if future tests removed
