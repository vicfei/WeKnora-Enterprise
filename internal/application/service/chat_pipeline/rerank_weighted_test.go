package chatpipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The weighted formula with default weights must reproduce the legacy
// hardcoded composite bit-for-bit, so that enable_weighted_ranking=true with
// no explicit weights is a no-op by construction.
func TestWeightedCompositeMatchesLegacyAtDefaults(t *testing.T) {
	defaults := (*types.RankingConfig)(nil).Resolve()
	sources := []string{"", "knowledge", "Web_Search"}
	for _, source := range sources {
		for m := 0.0; m <= 1.0001; m += 0.137 {
			for b := 0.0; b <= 1.0001; b += 0.211 {
				sr := &types.SearchResult{KnowledgeSource: source}
				legacy := compositeScore(sr, m, b)
				weighted := weightedCompositeScore(sr, m, b, 0, defaults)
				assert.Equal(t, legacy, weighted,
					"source=%q model=%.3f base=%.3f", source, m, b)
			}
		}
	}
}

func TestWeightedCompositeRespectsCustomWeights(t *testing.T) {
	// Unset weights fall back to the legacy defaults, so a partially
	// specified config still mixes every signal.
	partial := types.RankingConfig{RerankWeight: 1.0}
	r := partial.Resolve()
	got := weightedCompositeScore(&types.SearchResult{}, 0.42, 0.99, 0.9, r)
	assert.InDelta(t, 1.0*0.42+0.3*0.99+0.1*1.0+0*0.9, got, 1e-9)

	// A fully specified config is a plain weighted sum.
	full := types.RankingConfig{RerankWeight: 0.5, RetrievalWeight: 0.3, SourceWeight: 0.1, RecencyWeight: 0.1}
	r = full.Resolve()
	got = weightedCompositeScore(&types.SearchResult{}, 0.42, 0.99, 0.9, r)
	assert.InDelta(t, 0.5*0.42+0.3*0.99+0.1*1.0+0.1*0.9, got, 1e-9)

	// Clamping still applies with custom weights.
	both := types.RankingConfig{RerankWeight: 1.0, RetrievalWeight: 1.0}
	r = both.Resolve()
	got = weightedCompositeScore(&types.SearchResult{}, 0.9, 0.9, 0, r)
	assert.Equal(t, 1.0, got)
}

// Recency must be able to reorder otherwise-equal candidates: with the same
// rerank and retrieval scores, the fresher document wins once RecencyWeight
// is positive.
func TestWeightedCompositeRecencyBreaksTies(t *testing.T) {
	cfg := types.RankingConfig{RecencyWeight: 0.2}
	r := cfg.Resolve()
	fresh := weightedCompositeScore(&types.SearchResult{}, 0.5, 0.5, 1.0, r)
	stale := weightedCompositeScore(&types.SearchResult{}, 0.5, 0.5, 0.1, r)
	assert.Greater(t, fresh, stale)
}

func TestRecencySignalDecayAndEdges(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	mk := func(raw string) *types.SearchResult {
		sr := &types.SearchResult{Metadata: map[string]string{}}
		if raw != "" {
			sr.Metadata["knowledge_updated_at"] = raw
		}
		return sr
	}
	fresh := now.Format(time.RFC3339)
	halfLifeAgo := now.AddDate(0, 0, -180).Format(time.RFC3339)

	assert.Equal(t, 1.0, recencySignal(mk(fresh), 180, now))
	assert.InDelta(t, 0.5, recencySignal(mk(halfLifeAgo), 180, now), 1e-9,
		"one half-life ago must score 0.5")
	assert.Less(t, recencySignal(mk(now.AddDate(0, 0, -360).Format(time.RFC3339)), 180, now), 0.26,
		"two half-lives ago must approach 0.25")

	// Edge cases all yield a neutral 0.
	assert.Equal(t, 0.0, recencySignal(mk(""), 180, now), "missing timestamp")
	assert.Equal(t, 0.0, recencySignal(mk("not-a-date"), 180, now), "unparsable timestamp")
	assert.Equal(t, 0.0, recencySignal(mk(fresh), 0, now), "non-positive half-life")
	assert.Equal(t, 0.0, recencySignal(&types.SearchResult{}, 180, now), "nil metadata")
	// Future timestamps clamp to 1.
	assert.Equal(t, 1.0, recencySignal(mk(now.Add(time.Hour).Format(time.RFC3339)), 180, now))
}

// With lambda=1.0 MMR degenerates to pure relevance ordering; with the
// legacy 0.7 a redundant near-duplicate is demoted below a diverse candidate.
func TestApplyMMRLambdaControlsDiversityPenalty(t *testing.T) {
	build := func() []*types.SearchResult {
		return []*types.SearchResult{
			{ID: "A", Score: 0.9, Content: "alpha beta gamma delta"},
			{ID: "B", Score: 0.8, Content: "alpha beta gamma delta"},
			{ID: "C", Score: 0.7, Content: "zeta eta theta iota"},
		}
	}
	chatManage := &types.ChatManage{PipelineRequest: types.PipelineRequest{RerankTopK: 3}}

	pure := applyMMR(context.Background(), build(), chatManage, 3, 1.0)
	require.Equal(t, []string{"A", "B", "C"}, searchResultIDs(pure),
		"lambda=1 must keep pure score order")

	diverse := applyMMR(context.Background(), build(), chatManage, 3, 0.7)
	require.Equal(t, []string{"A", "C", "B"}, searchResultIDs(diverse),
		"legacy lambda must demote the near-duplicate below the diverse candidate")
}
