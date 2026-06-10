package pattern

import (
	"fmt"
	"io"
)

const (
	textEnvelopeMinSampleCoverage = 0.80
	textEnvelopeMinCoverage       = 0.50
)

type textEnvelopeTimestamp struct {
	Grok string
}

type textEnvelopeBody struct {
	Grok string
}

var textEnvelopeTimestamps = []textEnvelopeTimestamp{
	{Grok: `%{TIMESTAMP_ISO8601:timestamp}`},
	{Grok: `%{YEAR:year}/%{MONTHNUM2:month}/%{MONTHDAY2:day} %{TIME:time}`},
	{Grok: `%{MONTHDAY2:day}-%{MONTHNUM2:month}-%{YEAR:year} %{TIME:time}`},
	{Grok: `%{MONTHNUM2:month}-%{MONTHDAY2:day}-%{YEAR:year} %{TIME:time}`},
	{Grok: `%{MONTHDAY2:day}/%{MONTHNUM2:month}/%{YEAR:year} %{TIME:time}`},
	{Grok: `%{MONTHNUM2:month}/%{MONTHDAY2:day}/%{YEAR:year} %{TIME:time}`},
}

var textEnvelopeBodies = []textEnvelopeBody{
	{Grok: `\s+\[%{LOGLEVEL:level}\]\s+%{NOTSPACE:component}:\s*%{GREEDYDATA:message}`},
	{Grok: `\s+%{LOGLEVEL:level}\s+%{NOTSPACE:component}:\s*%{GREEDYDATA:message}`},
	{Grok: `\s+\[%{LOGLEVEL:level}\]\s+%{NOTSPACE:component}\s+-\s+%{GREEDYDATA:message}`},
	{Grok: `\s+%{LOGLEVEL:level}\s+%{NOTSPACE:component}\s+-\s+%{GREEDYDATA:message}`},
}

type textEnvelopeCandidate struct {
	Grok    string
	Matched int
	Typed   int
}

func inferTextEnvelope(sample []string) (string, bool) {
	if len(sample) == 0 {
		return "", false
	}

	var best *textEnvelopeCandidate
	for _, ts := range textEnvelopeTimestamps {
		for _, body := range textEnvelopeBodies {
			grok := ts.Grok + body.Grok
			re, err := CompileGrok(grok, nil)
			if err != nil {
				continue
			}
			matched := EvaluateCoverage(re, sample)
			if ratio(matched, len(sample)) < textEnvelopeMinSampleCoverage {
				continue
			}
			cand := &textEnvelopeCandidate{
				Grok:    grok,
				Matched: matched,
				Typed:   typedCaptureCount(grok),
			}
			if betterTextEnvelope(cand, best) {
				best = cand
			}
		}
	}
	if best == nil {
		return "", false
	}
	return best.Grok, true
}

func tryTextEnvelope(sample, all []string, diag io.Writer) *DiscoveredPattern {
	grok, ok := inferTextEnvelope(sample)
	if !ok {
		return nil
	}
	re, err := CompileGrok(grok, nil)
	if err != nil {
		fmt.Fprintf(diag, "text envelope: compile failed: %v\n", err)
		return nil
	}
	matched := EvaluateCoverage(re, all)
	cov := ratio(matched, len(all))
	if cov < textEnvelopeMinCoverage {
		fmt.Fprintf(diag, "text envelope: skipped weak candidate matched=%d/%d coverage=%.3f < %.2f\n",
			matched, len(all), cov, textEnvelopeMinCoverage)
		return nil
	}
	fmt.Fprintf(diag, "text envelope: matched=%d/%d\n", matched, len(all))
	return &DiscoveredPattern{
		Source:       "inferred:Text Envelope",
		SourceFamily: "inferred",
		Grok:         grok,
		Coverage:     cov,
		MatchedCount: matched,
		TotalLines:   len(all),
	}
}

func betterTextEnvelope(a, b *textEnvelopeCandidate) bool {
	if b == nil {
		return true
	}
	if a.Matched != b.Matched {
		return a.Matched > b.Matched
	}
	if a.Typed != b.Typed {
		return a.Typed > b.Typed
	}
	return len(a.Grok) > len(b.Grok)
}
