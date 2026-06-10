package benchmark

import (
	"strings"
	"testing"

	"github.com/logsonic/log2grok/internal/pattern"
)

type honestyCase struct {
	name         string
	lines        []string
	wantSource   string
	sourcePrefix string
}

func BenchmarkDiscoverHonestyPaths(b *testing.B) {
	cases := []honestyCase{
		{
			name: "short_log_level_one_offs",
			lines: []string{
				`ALERT backend/api error code=E42 retry=false host=web-1`,
				`WARN cache miss key=user:123 route=/v1/users latency=14ms`,
				`INFO worker done job=778 queue=email elapsed=9ms`,
			},
			wantSource: "fallback:Log Level Message",
		},
		{
			name: "short_http_no_timestamp",
			lines: []string{
				`GET /api/users 200 12ms`,
				`POST /api/jobs 202 31ms`,
				`DELETE /api/jobs/8 404 4ms`,
			},
			wantSource: "fallback:HTTP Request Summary",
		},
		{
			name: "useful_short_tiling",
			lines: []string{
				`worker alpha processed 17 jobs from queue fast`,
				`worker beta processed 22 jobs from queue slow`,
				`worker gamma processed 19 jobs from queue default`,
			},
			wantSource: "inferred:Tiled",
		},
		{
			name: "useful_short_mixed_shapes",
			lines: []string{
				`API request id=100 status=ok`,
				`API request id=101 status=fail`,
				`CACHE fill key=user:1 status=ok`,
				`CACHE fill key=user:2 status=miss`,
				`API request id=102 status=ok`,
				`CACHE fill key=user:3 status=ok`,
			},
			wantSource: "inferred:Tiled",
		},
		{
			name: "repeated_literal_clusters",
			lines: []string{
				`ERROR static api down`,
				`ERROR static api down`,
				`WARN static cache slow`,
				`WARN static cache slow`,
				`INFO static worker ready`,
				`INFO static worker ready`,
			},
			wantSource: "inferred:Tiled",
		},
		{
			name: "weak_ten_line_minority",
			lines: []string{
				`worker alpha processed 17 jobs`,
				`worker beta processed 18 jobs`,
				`cache miss user 1`,
				`db locked shard 7`,
				`payment declined card`,
				`email bounce mx`,
				`deploy started api`,
				`config reload ok`,
				`quota exceeded tenant`,
				`search timeout query`,
			},
			wantSource: "fallback:Message",
		},
	}

	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				dp, err := pattern.Discover(tc.lines, pattern.Options{})
				if err != nil {
					b.Fatalf("Discover: %v", err)
				}
				assertHonestSource(b, dp.Source, tc.wantSource, tc.sourcePrefix)
			}
		})
	}
}

func BenchmarkDiscoverMultiHonestyPaths(b *testing.B) {
	cases := []honestyCase{
		{
			name: "one_offs_fallback",
			lines: []string{
				`connection refused by db-primary at shard 7`,
				`cache warmed for tenant acme with 423 entries`,
				`job 812 started on worker runner-4`,
				`payment declined auth id ch_77 reason expired_card`,
			},
			wantSource: "fallback:Message",
		},
		{
			name: "supported_short_shape_patterns",
			lines: []string{
				`API request id=100 status=ok`,
				`API request id=101 status=fail`,
				`CACHE fill key=user:1 status=ok`,
				`CACHE fill key=user:2 status=miss`,
				`API request id=102 status=ok`,
				`CACHE fill key=user:3 status=ok`,
			},
			sourcePrefix: "inferred:",
		},
	}

	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, err := pattern.DiscoverMulti(tc.lines, pattern.Options{})
				if err != nil {
					b.Fatalf("DiscoverMulti: %v", err)
				}
				if len(res.Patterns) == 0 {
					b.Fatalf("DiscoverMulti returned no patterns")
				}
				assertHonestSource(b, res.Patterns[0].Source, tc.wantSource, tc.sourcePrefix)
			}
		})
	}
}

func assertHonestSource(tb testing.TB, got, want, prefix string) {
	tb.Helper()
	switch {
	case want != "" && got != want:
		tb.Fatalf("source = %q, want %q", got, want)
	case prefix != "" && !strings.HasPrefix(got, prefix):
		tb.Fatalf("source = %q, want prefix %q", got, prefix)
	}
}
