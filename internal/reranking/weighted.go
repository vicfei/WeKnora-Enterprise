package reranking

import (
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
)

// WeightedCompositeScore generalizes CompositeScore with configurable weights
// (fork asset: weighted-ranking). With the default RankingConfig it reproduces
// the legacy formula exactly (0.6*model + 0.3*base + 0.1*sourceWeight, plus an
// inert recency term of 0). Graph hits substitute the model score for the
// missing retrieval base, as CompositeScore does.
func WeightedCompositeScore(r *types.SearchResult, modelScore, baseScore, recency float64, ranking types.ResolvedRanking) float64 {
	sourceWeight := 1.0
	if strings.EqualFold(r.KnowledgeSource, "web_search") {
		sourceWeight = 0.95
	}
	if r.MatchType == types.MatchTypeGraph {
		baseScore = modelScore
	}
	return searchutil.CompositeScore(modelScore, baseScore, sourceWeight, recency, ranking)
}

// RecencySignal maps a document's time axis to an exponential decay signal
// in [0,1]: a document dated now scores 1, one half-life ago scores 0.5, and
// so on. Missing or unparsable timestamps yield 0, which is only meaningful
// when RecencyWeight > 0. Time-aware retrieval (设计 §4.5): the document's
// business_date is the primary axis when present, updated_at the fallback —
// the signal itself (searchutil.RecencySignal) is unchanged, only its input
// is upgraded.
func RecencySignal(r *types.SearchResult, halfLifeDays int, now time.Time) float64 {
	if r.Metadata == nil {
		return 0
	}
	// A parseable business_date wins even when its decayed signal is ~0 —
	// a 2020 rating being "old" is the business truth; only unparseable or
	// missing values may fall back to the system timestamp.
	if ts := r.Metadata["knowledge_business_date"]; ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil && !t.IsZero() {
			return searchutil.RecencySignal(ts, halfLifeDays, now)
		}
	}
	return searchutil.RecencySignal(r.Metadata["knowledge_updated_at"], halfLifeDays, now)
}
