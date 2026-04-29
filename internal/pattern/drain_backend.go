package pattern

import (
	"strings"

	"github.com/axiomhq/drain3"
)

// axiomDrain3Backend wraps github.com/axiomhq/drain3 behind the drainBackend
// adapter. Upstream API churn is contained here.
type axiomDrain3Backend struct {
	matcher *drain3.Matcher
	cfg     drain3.Config
}

func newAxiomDrain3Backend() *axiomDrain3Backend {
	cfg := drain3.DefaultConfig()
	cfg.SimilarityThreshold = 0.4
	cfg.MaxClusters = 0 // unlimited
	cfg.MaxTokens = 1024
	cfg.MaxBytes = 64 * 1024
	cfg.ExtraDelimiters = append([]string(nil), drainExtraDelimiters...)
	return &axiomDrain3Backend{cfg: cfg}
}

func (b *axiomDrain3Backend) Train(lines []string) error {
	m, err := drain3.TrainWithConfig(lines, b.cfg)
	if err != nil {
		return err
	}
	b.matcher = m
	return nil
}

func (b *axiomDrain3Backend) Templates() []drainTemplate {
	if b.matcher == nil {
		return nil
	}
	tmpls := b.matcher.Templates()
	out := make([]drainTemplate, 0, len(tmpls))
	for _, t := range tmpls {
		full := make([]string, t.TokenCount)
		denseIdx := 0
		for i := 0; i < t.TokenCount; i++ {
			if t.Params != nil && t.Params.Test(uint(i)) {
				full[i] = b.cfg.ParamString
			} else if denseIdx < len(t.Tokens) {
				full[i] = t.Tokens[denseIdx]
				denseIdx++
			}
		}
		out = append(out, drainTemplate{
			ID:        t.ID,
			Tokens:    full,
			LineCount: t.Count,
		})
	}
	return out
}

func (b *axiomDrain3Backend) ClusterIDOf(line string) (int, bool) {
	if b.matcher == nil {
		return 0, false
	}
	id, ok := b.matcher.MatchID(line)
	return id, ok
}

func (b *axiomDrain3Backend) Tokenize(line string) []string {
	// Mirror drain3's tokenize() logic: replace extra delimiters with spaces,
	// then split on whitespace runs.
	for _, d := range b.cfg.ExtraDelimiters {
		line = strings.ReplaceAll(line, d, " ")
	}
	return strings.Fields(line)
}
