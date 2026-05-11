package log2grok

import (
	"errors"
	"strings"
	"testing"
)

const nginxLine = `192.168.1.1 - - [23/Jan/2023:14:05:01 +0000] "GET /index.html HTTP/1.1" 200 1234 "http://google.com" "Mozilla/5.0"`

const nginxGrok = `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`

func TestNewDecoder_RejectsEmptyPattern(t *testing.T) {
	if _, err := NewDecoder(PatternSpec{Name: "x"}, DecoderOptions{}); !errors.Is(err, ErrEmptyPattern) {
		t.Fatalf("want ErrEmptyPattern, got %v", err)
	}
}

func TestNewDecoder_InvalidGrokRefSurfacesError(t *testing.T) {
	_, err := NewDecoder(PatternSpec{Name: "broken", Grok: `%{NO_SUCH_PRIM:f}`}, DecoderOptions{})
	if err == nil {
		t.Fatal("expected compile error for unknown primitive")
	}
	if !strings.Contains(err.Error(), "NO_SUCH_PRIM") {
		t.Errorf("error should mention the missing primitive: %v", err)
	}
}

func TestDecoder_NginxLine_AllFieldsCaptured(t *testing.T) {
	dec, err := NewDecoder(PatternSpec{Name: "Nginx Access", Grok: nginxGrok}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}

	results := dec.Decode([]string{nginxLine})
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Matched {
		t.Fatalf("expected match, got error %q", r.Error)
	}
	if r.Pattern != "Nginx Access" {
		t.Errorf("pattern name: got %q", r.Pattern)
	}
	if r.Raw != nginxLine {
		t.Errorf("raw not preserved")
	}
	if r.Smart != nil {
		t.Errorf("Smart should be nil when SmartDecode is off, got %v", r.Smart)
	}
	cases := map[string]string{
		"client_ip":    "192.168.1.1",
		"method":       "GET",
		"url":          "/index.html",
		"http_version": "1.1",
		"status":       "200",
		"bytes":        "1234",
	}
	for k, want := range cases {
		if got := r.Fields[k]; got != want {
			t.Errorf("Fields[%q]: want %q, got %q", k, want, got)
		}
	}
}

func TestDecoder_SmartDecodeAddsAuxFields(t *testing.T) {
	const line = `User alice@example.com from 10.0.0.5 hit https://example.com/api id=550e8400-e29b-41d4-a716-446655440000`
	const grok = `%{GREEDYDATA:message}`

	dec, err := NewDecoder(PatternSpec{Grok: grok}, DecoderOptions{SmartDecode: true})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	r := dec.Decode([]string{line})[0]
	if !r.Matched {
		t.Fatalf("expected match")
	}
	if r.Smart == nil {
		t.Fatal("Smart should be populated")
	}
	for _, key := range []string{"_ipv4_addr", "_email_addr", "_urls", "_uuids"} {
		if r.Smart[key] == "" {
			t.Errorf("Smart[%q] missing in %v", key, r.Smart)
		}
	}
	if r.Smart["_ipv4_addr"] != "10.0.0.5" {
		t.Errorf("ipv4: got %q", r.Smart["_ipv4_addr"])
	}
}

func TestDecoder_TypedSmartEntitiesParallelMap(t *testing.T) {
	const line = `User alice@example.com from 10.0.0.5 hit https://example.com/api id=550e8400-e29b-41d4-a716-446655440000 mac=aa:bb:cc:dd:ee:ff ipv6=2001:db8::1`
	dec, err := NewDecoder(PatternSpec{Grok: `%{GREEDYDATA:message}`}, DecoderOptions{SmartDecode: true})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	r := dec.Decode([]string{line})[0]
	if !r.Matched {
		t.Fatalf("expected match")
	}
	if !r.Entities.Any() {
		t.Fatal("Entities.Any() should be true")
	}
	if len(r.Entities.IPv4) != 1 || r.Entities.IPv4[0] != "10.0.0.5" {
		t.Errorf("IPv4: %v", r.Entities.IPv4)
	}
	if len(r.Entities.IPv6) == 0 || r.Entities.IPv6[0] != "2001:db8::1" {
		t.Errorf("IPv6: %v", r.Entities.IPv6)
	}
	if len(r.Entities.Emails) != 1 || r.Entities.Emails[0] != "alice@example.com" {
		t.Errorf("Emails: %v", r.Entities.Emails)
	}
	if len(r.Entities.URLs) != 1 {
		t.Errorf("URLs: %v", r.Entities.URLs)
	}
	if len(r.Entities.MACs) != 1 || r.Entities.MACs[0] != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MACs: %v", r.Entities.MACs)
	}
	if len(r.Entities.UUIDs) != 1 {
		t.Errorf("UUIDs: %v", r.Entities.UUIDs)
	}
	// Map should stay in sync.
	if r.Smart["_ipv4_addr"] != "10.0.0.5" {
		t.Errorf("map _ipv4_addr mismatch: %q", r.Smart["_ipv4_addr"])
	}
	if !strings.Contains(r.Smart["_urls"], "https://example.com/api") {
		t.Errorf("map _urls: %q", r.Smart["_urls"])
	}
}

func TestDecoder_SmartDecode_NoEntitiesYieldsZeroValue(t *testing.T) {
	dec, _ := NewDecoder(PatternSpec{Grok: `%{GREEDYDATA:m}`}, DecoderOptions{SmartDecode: true})
	r := dec.Decode([]string{"plain log message with nothing interesting"})[0]
	if r.Smart != nil {
		t.Errorf("Smart should stay nil when no entities found, got %v", r.Smart)
	}
	if r.Entities.Any() {
		t.Errorf("Entities.Any() should be false, got %+v", r.Entities)
	}
}

func TestDecoder_SmartURLStopsAtClosingPunct(t *testing.T) {
	// URLs commonly appear inside parens/quotes in log lines; the regex
	// must not gobble the trailing delimiter.
	const line = `request to (https://example.com/foo) completed`
	dec, _ := NewDecoder(PatternSpec{Grok: `%{GREEDYDATA:m}`}, DecoderOptions{SmartDecode: true})
	r := dec.Decode([]string{line})[0]
	if len(r.Entities.URLs) != 1 || r.Entities.URLs[0] != "https://example.com/foo" {
		t.Errorf("URLs: %v", r.Entities.URLs)
	}
}

func TestDecoder_UnmatchedLine_ErrorPopulated(t *testing.T) {
	dec, err := NewDecoder(PatternSpec{Name: "ipv4-only", Grok: `%{IP:addr}`}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	r := dec.Decode([]string{"not an address at all"})[0]
	if r.Matched {
		t.Fatal("expected miss")
	}
	if r.Error == "" {
		t.Error("error should be populated on miss")
	}
	if r.Fields != nil {
		t.Error("Fields should be nil on miss")
	}
}

func TestDecodeReader_StreamsAllLines(t *testing.T) {
	dec, err := NewDecoder(PatternSpec{Grok: `%{INT:n}`}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	input := "1\n2\n3\nfoo\n4\n"
	var seen []LineResult
	matched, failed, err := dec.DecodeReader(strings.NewReader(input), func(r LineResult) error {
		seen = append(seen, r)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeReader: %v", err)
	}
	if matched != 4 || failed != 1 {
		t.Errorf("counts: matched=%d failed=%d (want 4/1)", matched, failed)
	}
	if len(seen) != 5 {
		t.Errorf("callback should fire once per line, got %d", len(seen))
	}
}

func TestDecodeConcurrent_MatchesSerialOutput(t *testing.T) {
	dec, _ := NewDecoder(PatternSpec{Grok: nginxGrok}, DecoderOptions{})
	// Big batch to push past concurrentDecodeMinBatch.
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = nginxLine
	}
	lines[5] = "garbage line that won't match"
	lines[1500] = "another garbage line"

	serial := dec.Decode(lines)
	parallel := dec.DecodeConcurrent(lines, 4)
	if len(serial) != len(parallel) {
		t.Fatalf("len mismatch: %d vs %d", len(serial), len(parallel))
	}
	for i := range serial {
		if serial[i].Matched != parallel[i].Matched {
			t.Errorf("idx %d: Matched serial=%v parallel=%v", i, serial[i].Matched, parallel[i].Matched)
		}
		if serial[i].Raw != parallel[i].Raw {
			t.Errorf("idx %d: order broken", i)
		}
	}
}

func TestDecodeConcurrent_SmallBatchFallsBackToSerial(t *testing.T) {
	dec, _ := NewDecoder(PatternSpec{Grok: `%{INT:n}`}, DecoderOptions{})
	r := dec.DecodeConcurrent([]string{"1", "2", "3"}, 4)
	if len(r) != 3 {
		t.Errorf("count: %d", len(r))
	}
	for i, x := range r {
		if !x.Matched {
			t.Errorf("idx %d not matched", i)
		}
	}
}

func BenchmarkDecode_Serial(b *testing.B) {
	dec, _ := NewDecoder(PatternSpec{Grok: nginxGrok}, DecoderOptions{})
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = nginxLine
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dec.Decode(lines)
	}
}

func BenchmarkDecode_Concurrent(b *testing.B) {
	dec, _ := NewDecoder(PatternSpec{Grok: nginxGrok}, DecoderOptions{})
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = nginxLine
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dec.DecodeConcurrent(lines, 0)
	}
}

func TestDecodeReader_CallbackErrorAborts(t *testing.T) {
	dec, _ := NewDecoder(PatternSpec{Grok: `%{INT:n}`}, DecoderOptions{})
	stop := errors.New("boom")
	matched, failed, err := dec.DecodeReader(strings.NewReader("1\n2\n3\n"), func(_ LineResult) error {
		return stop
	})
	if err != stop {
		t.Errorf("want stop sentinel, got %v", err)
	}
	if matched != 1 || failed != 0 {
		t.Errorf("should have stopped after first line: matched=%d failed=%d", matched, failed)
	}
}
