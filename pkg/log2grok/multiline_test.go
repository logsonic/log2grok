package log2grok

import (
	"errors"
	"strings"
	"testing"
)

func TestJoinMultiline_HeaderMode_JavaStackTrace(t *testing.T) {
	input := `2026-03-15 10:20:30 ERROR boom
	at com.foo.Bar.baz(Bar.java:42)
	at com.foo.Qux.run(Qux.java:7)
Caused by: java.lang.NullPointerException
	at com.foo.Bar.bar(Bar.java:30)
2026-03-15 10:20:31 INFO recovered`
	cfg := CommonMultilineConfigs()["iso8601"]
	out, err := JoinMultiline(strings.NewReader(input), cfg)
	if err != nil {
		t.Fatalf("JoinMultiline: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 records, got %d: %#v", len(out), out)
	}
	if !strings.Contains(out[0], "ERROR boom") || !strings.Contains(out[0], "Caused by") {
		t.Errorf("record 0 missing fields: %q", out[0])
	}
	if !strings.HasPrefix(out[1], "2026-03-15 10:20:31") {
		t.Errorf("record 1 wrong: %q", out[1])
	}
}

func TestJoinMultiline_IndentMode(t *testing.T) {
	input := `event1
 continuation a
 continuation b
event2`
	out, err := JoinMultiline(strings.NewReader(input), MultilineConfig{Mode: MultilineIndent})
	if err != nil {
		t.Fatalf("JoinMultiline: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	if !strings.Contains(out[0], "continuation b") {
		t.Errorf("indent fold lost continuation: %q", out[0])
	}
}

func TestJoinMultiline_CustomPredicate(t *testing.T) {
	input := `evt1
> cont1
> cont2
evt2`
	out, _ := JoinMultiline(strings.NewReader(input), MultilineConfig{
		Mode: MultilineCustom,
		IsContinuation: func(line string) bool {
			return strings.HasPrefix(line, "> ")
		},
	})
	if len(out) != 2 {
		t.Fatalf("custom predicate fold: %v", out)
	}
}

func TestJoinMultiline_MaxLinesCapsRecord(t *testing.T) {
	var b strings.Builder
	b.WriteString("HDR\n")
	for i := 0; i < 200; i++ {
		b.WriteString(" cont\n")
	}
	out, _ := JoinMultiline(strings.NewReader(b.String()), MultilineConfig{Mode: MultilineIndent, MaxLines: 50})
	if len(out) < 2 {
		t.Fatalf("MaxLines should split: %d", len(out))
	}
	if strings.Count(out[0], "cont") > 50 {
		t.Errorf("record 0 exceeded MaxLines: %d", strings.Count(out[0], "cont"))
	}
}

func TestJoinMultiline_InvalidConfig(t *testing.T) {
	_, err := JoinMultiline(strings.NewReader(""), MultilineConfig{Mode: MultilineHeader})
	if !errors.Is(err, ErrMultilineConfig) {
		t.Errorf("want ErrMultilineConfig, got %v", err)
	}
	_, err = JoinMultiline(strings.NewReader(""), MultilineConfig{Mode: MultilineCustom})
	if !errors.Is(err, ErrMultilineConfig) {
		t.Errorf("want ErrMultilineConfig (custom), got %v", err)
	}
}

func TestJoinMultilineStrings_RoundTripsThroughDecoder(t *testing.T) {
	lines := []string{
		"2026-03-15 10:20:30 ERROR boom",
		"\tat foo.Bar.baz(Bar.java:42)",
		"2026-03-15 10:20:31 INFO ok",
	}
	folded, err := JoinMultilineStrings(lines, CommonMultilineConfigs()["iso8601"])
	if err != nil {
		t.Fatalf("JoinMultilineStrings: %v", err)
	}
	if len(folded) != 2 {
		t.Fatalf("fold count: %d", len(folded))
	}
	dec, err := NewDecoder(PatternSpec{
		Grok: `%{TIMESTAMP_ISO8601:ts} %{LOGLEVEL:lvl} %{GREEDYDATA:msg}`,
	}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	r := dec.Decode(folded)
	if !r[0].Matched || !r[1].Matched {
		t.Errorf("decoder didn't accept folded lines: %+v", r)
	}
}
