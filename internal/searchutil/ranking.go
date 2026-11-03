// ranking.go is the single shared kernel for the retrieval-ranking signals
// that both QA channels consume: the weighted composite score, the recency
// decay signal, and MMR selection. Before this file existed the chat pipeline
// (chat_pipeline/rerank.go) and the agent tool (agent/tools/knowledge_search.go)
// each carried a private copy of the same math, which had already drifted
// (upstream findings register U-03). Both call sites now delegate here; a
// zero-value ResolvedRanking is never passed by the callers — they resolve
// through RankingConfig.Resolve(), which fills legacy defaults.
package searchutil

import (
	"math"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// CompositeScore computes the weighted relevance score for one candidate:
//
//	r.RerankWeight*modelScore + r.RetrievalWeight*baseScore +
//	r.SourceWeight*sourceWeight + r.RecencyWeight*recency, clamped to [0,1].
//
// With the default RankingConfig it reproduces the legacy formula
// 0.6*model + 0.3*base + 0.1*sourceWeight exactly (recency term inert at 0).
// sourceWeight is the per-source constant (web_search hits are discounted to
// 0.95 by both call sites); recency comes from RecencySignal.
func CompositeScore(modelScore, baseScore, sourceWeight, recency float64, r types.ResolvedRanking) float64 {
	composite := r.RerankWeight*modelScore + r.RetrievalWeight*baseScore +
		r.SourceWeight*sourceWeight + r.RecencyWeight*recency
	if composite < 0 {
		composite = 0
	}
	if composite > 1 {
		composite = 1
	}
	return composite
}

// RecencySignal maps a document's last-update timestamp (RFC3339, as enriched
// into SearchResult.Metadata["knowledge_updated_at"]) to an exponential decay
// signal in [0,1]: updated now scores 1, one half-life ago scores 0.5. Missing
// or unparsable timestamps yield 0, which is only meaningful when
// RecencyWeight > 0. halfLifeDays <= 0 disables the signal.
func RecencySignal(updatedAtRaw string, halfLifeDays int, now time.Time) float64 {
	if halfLifeDays <= 0 || updatedAtRaw == "" {
		return 0
	}
	updatedAt, err := time.Parse(time.RFC3339, updatedAtRaw)
	if err != nil || updatedAt.IsZero() {
		return 0
	}
	ageDays := now.Sub(updatedAt).Hours() / 24
	if ageDays <= 0 {
		return 1
	}
	return math.Pow(2, -ageDays/float64(halfLifeDays))
}

// SelectMMR runs Maximal Marginal Relevance over pre-computed candidate
// scores and token sets, returning the indices of the k selected candidates
// in selection order. Selection is the incremental form: maxRedundancy[i]
// caches candidate i's maximum Jaccard against everything selected so far,
// so each round costs one comparison per remaining candidate instead of one
// per (candidate, selected) pair. Candidate iteration order (hence
// tie-breaking) matches the naive pairwise form both call sites used before.
func SelectMMR(k int, lambda float64, scores []float64, tokenSets []map[string]struct{}) []int {
	n := len(scores)
	if k <= 0 || n == 0 || len(tokenSets) != n {
		return nil
	}
	selected := make([]int, 0, k)
	maxRedundancy := make([]float64, n)
	remaining := make([]int, n)
	for i := range remaining {
		remaining[i] = i
	}
	for len(selected) < k && len(remaining) > 0 {
		bestPos := 0
		bestScore := -1.0
		for pos, i := range remaining {
			mmr := lambda*scores[i] - (1.0-lambda)*maxRedundancy[i]
			if mmr > bestScore {
				bestScore = mmr
				bestPos = pos
			}
		}
		chosen := remaining[bestPos]
		selected = append(selected, chosen)
		chosenTokens := tokenSets[chosen]
		remaining = append(remaining[:bestPos], remaining[bestPos+1:]...)
		for _, i := range remaining {
			if j := Jaccard(tokenSets[i], chosenTokens); j > maxRedundancy[i] {
				maxRedundancy[i] = j
			}
		}
	}
	return selected
}
