package pattern

import (
	"strings"
	"testing"
)

func TestParseLogfmtPairs(t *testing.T) {
	cases := []struct {
		line   string
		want   []logfmtPair
		wantOk bool
	}{
		{
			line:   "a=1 b=2 c=3",
			wantOk: true,
			want: []logfmtPair{
				{Key: "a", Value: "1"},
				{Key: "b", Value: "2"},
				{Key: "c", Value: "3"},
			},
		},
		{
			line:   `msg="hello world" count=42`,
			wantOk: true,
			want: []logfmtPair{
				{Key: "msg", Value: "hello world", Quote: '"'},
				{Key: "count", Value: "42"},
			},
		},
		{
			line:   `name=foo bar=baz`, // bare value with no quotes
			wantOk: true,
			want: []logfmtPair{
				{Key: "name", Value: "foo"},
				{Key: "bar", Value: "baz"},
			},
		},
		{
			line:   "key=", // empty value after =
			wantOk: false,
		},
		{
			line:   "a=1  b=2", // double space
			wantOk: false,
		},
		{
			line:   "bareword a=1", // bare word before key=value
			wantOk: false,
		},
		{
			line:   "a=1 b=2 ", // trailing space
			wantOk: false,
		},
		{
			line:   `k='single quoted'`,
			wantOk: true,
			want: []logfmtPair{
				{Key: "k", Value: "single quoted", Quote: '\''},
			},
		},
		{
			line:   `k="escaped \"quote\""`,
			wantOk: true,
			want: []logfmtPair{
				{Key: "k", Value: `escaped \"quote\"`, Quote: '"'},
			},
		},
	}
	for _, tc := range cases {
		pairs, ok := parseLogfmtPairs(tc.line)
		if ok != tc.wantOk {
			t.Fatalf("parseLogfmtPairs(%q) ok=%v, want %v", tc.line, ok, tc.wantOk)
		}
		if !tc.wantOk {
			continue
		}
		if len(pairs) != len(tc.want) {
			t.Fatalf("parseLogfmtPairs(%q) len=%d, want %d: %#v", tc.line, len(pairs), len(tc.want), pairs)
		}
		for i := range pairs {
			if pairs[i].Key != tc.want[i].Key || pairs[i].Value != tc.want[i].Value || pairs[i].Quote != tc.want[i].Quote {
				t.Fatalf("pair[%d]: got %#v, want %#v", i, pairs[i], tc.want[i])
			}
		}
	}
}

func TestRenderLogfmtKeyed(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		wantOk bool
		wantIn []string // substrings that must appear in the emitted grok
	}{
		{
			name: "simple_pairs",
			lines: []string{
				"a=1 b=2 c=hello",
				"a=9 b=4 c=world",
			},
			wantOk: true,
			wantIn: []string{"a=", "b=", "c="},
		},
		{
			name: "typed_values",
			lines: []string{
				"count=42 status=200 msg=ok",
				"count=7 status=404 msg=notfound",
			},
			wantOk: true,
			wantIn: []string{"count=%{INT:", "status=%{INT:", "msg=%{WORD:"},
		},
		{
			name: "quoted_values",
			lines: []string{
				`msg="hello world" path=/api`,
				`msg="goodbye" path=/health`,
			},
			wantOk: true,
			wantIn: []string{`msg=%{QUOTEDSTRING:`, `path=%{URIPATHPARAM:`},
		},
		{
			name: "dotted_keys",
			lines: []string{
				"req.method=GET req.path=/api",
				"req.method=POST req.path=/upload",
			},
			wantOk: true,
			wantIn: []string{`req\.method=%{WORD:req_method}`, `req\.path=%{URIPATHPARAM:req_path}`},
		},
		{
			name: "mismatched_keys",
			lines: []string{
				"a=1 b=2",
				"a=1 c=3",
			},
			wantOk: false,
		},
		{
			name: "too_few_lines",
			lines: []string{
				"a=1 b=2",
			},
			wantOk: false,
		},
		{
			name: "duplicate_keys",
			lines: []string{
				"msg=outer msg=hello",
				"msg=outer msg=world",
			},
			wantOk: true,
			wantIn: []string{"msg=%{WORD:message} msg=%{WORD:message_2}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grok, ok := renderLogfmtKeyed(tc.lines)
			if ok != tc.wantOk {
				t.Fatalf("ok=%v, want %v; grok=%q", ok, tc.wantOk, grok)
			}
			if !tc.wantOk {
				return
			}
			for _, sub := range tc.wantIn {
				if !strings.Contains(grok, sub) {
					t.Fatalf("grok missing %q; got: %s", sub, grok)
				}
			}
			re, err := CompileGrok(grok, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			for _, line := range tc.lines {
				if !re.MatchString(line) {
					t.Fatalf("grok does not match line %q", line)
				}
			}
		})
	}
}

func TestLogfmtFieldNameDedupe(t *testing.T) {
	used := map[string]int{}
	names := []string{
		logfmtFieldName("msg", used),
		logfmtFieldName("msg", used),
		logfmtFieldName("msg", used),
	}
	want := []string{"message", "message_2", "message_3"}
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("name[%d]=%q, want %q", i, names[i], want[i])
		}
	}
}
