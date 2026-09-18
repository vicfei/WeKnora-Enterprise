package chatpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PluginRerank implements reranking functionality for chat pipeline
type PluginRerank struct {
	modelService interfaces.ModelService // Service to access rerank models
}

// NewPluginRerank creates a new rerank plugin instance
func NewPluginRerank(eventManager *EventManager, modelService interfaces.ModelService) *PluginRerank {
	res := &PluginRerank{
		modelService: modelService,
	}
	eventManager.Register(res)
	return res
}

// ActivationEvents returns the event types this plugin handles
func (p *PluginRerank) ActivationEvents() []types.EventType {
	return []types.EventType{types.CHUNK_RERANK}
}

// OnEvent handles reranking events in the chat pipeline
func (p *PluginRerank) OnEvent(ctx context.Context,
	eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	if !chatManage.NeedsRetrieval() {
		return next()
	}
	pipelineInfo(ctx, "Rerank", "input", map[string]interface{}{
		"session_id":    chatManage.SessionID,
		"candidate_cnt": len(chatManage.SearchResult),
		"rerank_model":  chatManage.RerankModelID,
		"rerank_thresh": chatManage.RerankThreshold,
		"rewrite_query": chatManage.RewriteQuery,
	})
	if len(chatManage.SearchResult) == 0 {
		pipelineInfo(ctx, "Rerank", "skip", map[string]interface{}{
			"reason": "empty_search_result",
		})
		return next()
	}
	if chatManage.RerankModelID == "" {
		pipelineWarn(ctx, "Rerank", "skip", map[string]interface{}{
			"reason": "empty_model_id",
		})
		return next()
	}

	// Get rerank model from service
	rerankModel, err := p.modelService.GetRerankModel(ctx, chatManage.RerankModelID)
	if err != nil {
		pipelineError(ctx, "Rerank", "get_model", map[string]interface{}{
			"model_id": chatManage.RerankModelID,
			"error":    err.Error(),
		})
		return ErrGetRerankModel.WithError(err)
	}

	// Prepare passages for reranking.
	var passages []string
	var candidatesToRerank []*types.SearchResult

	for _, result := range chatManage.SearchResult {
		passage := getEnrichedPassage(ctx, result)
		if strings.TrimSpace(passage) == "" {
			pipelineInfo(ctx, "Rerank", "empty_passage_skip", map[string]interface{}{
				"chunk_id": result.ID,
			})
			continue
		}
		passages = append(passages, passage)
		candidatesToRerank = append(candidatesToRerank, result)
	}

	passagesPreview := langfuse.SummarizePassagePreviews(candidatesToRerank, passages, 25)
	rerankCtx, rerankSpan := langfuse.GetManager().StartSpan(ctx, langfuse.SpanOptions{
		Name: "rerank",
		Input: map[string]interface{}{
			"query":            chatManage.RewriteQuery,
			"candidate_count":  len(candidatesToRerank),
			"rerank_model_id":  chatManage.RerankModelID,
			"threshold":        chatManage.RerankThreshold,
			"rerank_top_k":     chatManage.RerankTopK,
			"faq_priority":     chatManage.FAQPriorityEnabled,
			"faq_score_boost":  chatManage.FAQScoreBoost,
			"passages_preview": passagesPreview,
		},
		Metadata: map[string]interface{}{
			"session_id": chatManage.SessionID,
		},
	})
	ctx = rerankCtx
	spanOutput := map[string]interface{}{}
	var spanErr error
	defer func() {
		rerankSpan.Finish(spanOutput, nil, spanErr)
	}()

	pipelineInfo(ctx, "Rerank", "build_passages", map[string]interface{}{
		"total_cnt":     len(chatManage.SearchResult),
		"candidate_cnt": len(candidatesToRerank),
	})

	var rerankResp []rerank.RankResult
	var rawRerankResp []rerank.RankResult
	thresholdDegraded := false

	// Only call rerank model if there are candidates
	if len(candidatesToRerank) > 0 {
		// Single rerank call with RewriteQuery, use threshold degradation if no results
		originalThreshold := chatManage.RerankThreshold
		var rerankErr error
		rerankResp, rerankErr = p.rerank(ctx, chatManage, rerankModel, chatManage.RewriteQuery, passages, candidatesToRerank)

		if rerankErr != nil {
			// Rerank API failed — fallback to original retrieval results so the
			// pipeline can still return something useful to the caller.
			pipelineWarn(ctx, "Rerank", "api_error_fallback", map[string]interface{}{
				"error":         rerankErr.Error(),
				"candidate_cnt": len(candidatesToRerank),
			})
			chatManage.SearchResult = candidatesToRerank
			spanOutput = map[string]interface{}{
				"stage":           "api_error_fallback",
				"candidate_count": len(candidatesToRerank),
				"error":           rerankErr.Error(),
			}
			return next()
		}
		rawRerankResp = append([]rerank.RankResult(nil), rerankResp...)

		// If no results and threshold is high enough, try with lower threshold
		if len(rerankResp) == 0 && originalThreshold > 0.3 {
			thresholdDegraded = true
			degradedThreshold := originalThreshold * 0.7
			if degradedThreshold < 0.3 {
				degradedThreshold = 0.3
			}
			pipelineWarn(ctx, "Rerank", "threshold_degrade", map[string]interface{}{
				"original":      originalThreshold,
				"degraded":      degradedThreshold,
				"candidate_cnt": len(candidatesToRerank),
				"reason":        "no results above original threshold, retrying with lower threshold",
			})
			chatManage.RerankThreshold = degradedThreshold
			rerankResp, rerankErr = p.rerank(ctx, chatManage, rerankModel, chatManage.RewriteQuery, passages, candidatesToRerank)
			// Restore original threshold
			chatManage.RerankThreshold = originalThreshold
			if rerankErr != nil {
				pipelineWarn(ctx, "Rerank", "api_error_fallback", map[string]interface{}{
					"error":         rerankErr.Error(),
					"candidate_cnt": len(candidatesToRerank),
				})
				chatManage.SearchResult = candidatesToRerank
				spanOutput = map[string]interface{}{
					"stage":              "api_error_fallback",
					"candidate_count":    len(candidatesToRerank),
					"threshold_degraded": thresholdDegraded,
					"error":              rerankErr.Error(),
				}
				return next()
			}
			rawRerankResp = append([]rerank.RankResult(nil), rerankResp...)
		}
	}

	pipelineInfo(ctx, "Rerank", "model_response", map[string]interface{}{
		"result_cnt": len(rerankResp),
	})

	logRerankInputScoreSample(ctx, chatManage.SearchResult)

	// 二开特性（多维度加权排序）：开启时用可配置权重替代旧版硬编码公式；
	// 关闭时走原 compositeScore，一个常量都不变。
	var ranking types.ResolvedRanking
	if chatManage.EnableWeightedRanking {
		ranking = chatManage.Ranking.Resolve()
		pipelineInfo(ctx, "Rerank", "weighted_ranking", map[string]interface{}{
			"rerank_weight":          ranking.RerankWeight,
			"retrieval_weight":       ranking.RetrievalWeight,
			"source_weight":          ranking.SourceWeight,
			"recency_weight":         ranking.RecencyWeight,
			"recency_half_life_days": ranking.RecencyHalfLifeDays,
			"mmr_lambda":             ranking.MMRLambda,
		})
	}

	for i := range chatManage.SearchResult {
		chatManage.SearchResult[i].Metadata = ensureMetadata(chatManage.SearchResult[i].Metadata)
	}
	reranked := make([]*types.SearchResult, 0, len(rerankResp))

	// Process reranked results
	for _, rr := range rerankResp {
		if rr.Index >= len(candidatesToRerank) {
			continue
		}
		sr := candidatesToRerank[rr.Index]
		base := sr.Score
		sr.Metadata["base_score"] = fmt.Sprintf("%.4f", base)
		modelScore := rr.RelevanceScore
		sr.Metadata["model_score"] = fmt.Sprintf("%.4f", modelScore)
		if chatManage.EnableWeightedRanking {
			recency := recencySignal(sr, ranking.RecencyHalfLifeDays, time.Now())
			sr.Metadata["recency"] = fmt.Sprintf("%.4f", recency)
			sr.Score = weightedCompositeScore(sr, modelScore, base, recency, ranking)
		} else {
			sr.Score = compositeScore(sr, modelScore, base)
		}

		// Apply FAQ score boost if enabled
		if chatManage.FAQPriorityEnabled && chatManage.FAQScoreBoost > 1.0 &&
			sr.ChunkType == string(types.ChunkTypeFAQ) {
			originalScore := sr.Score
			sr.Score = math.Min(sr.Score*chatManage.FAQScoreBoost, 1.0)
			sr.Metadata["faq_boosted"] = "true"
			sr.Metadata["faq_original_score"] = fmt.Sprintf("%.4f", originalScore)
			pipelineInfo(ctx, "Rerank", "faq_boost", map[string]interface{}{
				"chunk_id":       sr.ID,
				"original_score": fmt.Sprintf("%.4f", originalScore),
				"boosted_score":  fmt.Sprintf("%.4f", sr.Score),
				"boost_factor":   chatManage.FAQScoreBoost,
			})
		}

		reranked = append(reranked, sr)
	}

	mmrLambda := types.LegacyMMRLambda
	if chatManage.EnableWeightedRanking {
		mmrLambda = ranking.MMRLambda
	}
	final := applyMMR(ctx, reranked, chatManage, min(len(reranked), max(1, chatManage.RerankTopK)), mmrLambda)
	chatManage.RerankResult = final

	// 信号分布观测（roadmap 主题 3：先加日志观察各信号分布再定默认权重）。
	// 每轮一条汇总行；recency 仅在加权排序开启时存在。
	logSignalDistribution(ctx, reranked)

	// Log composite top scores and MMR selection summary
	topN := min(3, len(reranked))
	for i := 0; i < topN; i++ {
		pipelineInfo(ctx, "Rerank", "composite_top", map[string]interface{}{
			"rank":        i + 1,
			"chunk_id":    reranked[i].ID,
			"base_score":  reranked[i].Metadata["base_score"],
			"final_score": fmt.Sprintf("%.4f", reranked[i].Score),
		})
	}

	if len(chatManage.RerankResult) == 0 {
		pipelineWarn(ctx, "Rerank", "output", map[string]interface{}{
			"filtered_cnt": 0,
		})
		spanOutput = buildRerankSpanOutput(
			candidatesToRerank,
			passages,
			rawRerankResp,
			reranked,
			nil,
			chatManage,
			thresholdDegraded,
		)
		return ErrSearchNothing
	}

	spanOutput = buildRerankSpanOutput(
		candidatesToRerank,
		passages,
		rawRerankResp,
		reranked,
		chatManage.RerankResult,
		chatManage,
		thresholdDegraded,
	)
	pipelineInfo(ctx, "Rerank", "output", map[string]interface{}{
		"filtered_cnt": len(chatManage.RerankResult),
	})
	return next()
}

func buildRerankSpanOutput(
	candidates []*types.SearchResult,
	passages []string,
	modelScores []rerank.RankResult,
	composite []*types.SearchResult,
	final []*types.SearchResult,
	chatManage *types.ChatManage,
	thresholdDegraded bool,
) map[string]interface{} {
	modelRows := make([]map[string]interface{}, 0, len(modelScores))
	for i, rr := range modelScores {
		row := map[string]interface{}{
			"rank":        i + 1,
			"index":       rr.Index,
			"model_score": rr.RelevanceScore,
		}
		if rr.Index >= 0 && rr.Index < len(candidates) {
			row["chunk_id"] = candidates[rr.Index].ID
			row["knowledge_id"] = candidates[rr.Index].KnowledgeID
			row["knowledge_title"] = candidates[rr.Index].KnowledgeTitle
			row["match_type"] = candidates[rr.Index].MatchType
			row["retrieval_score"] = candidates[rr.Index].Score
			if rr.Index < len(passages) {
				row["preview"] = langfuse.TruncateRunes(passages[rr.Index], 160)
			}
		}
		modelRows = append(modelRows, row)
	}

	out := map[string]interface{}{
		"candidate_count":    len(candidates),
		"model_result_count": len(modelScores),
		"composite_count":    len(composite),
		"final_count":        len(final),
		"threshold":          chatManage.RerankThreshold,
		"rerank_top_k":       chatManage.RerankTopK,
		"threshold_degraded": thresholdDegraded,
		"model_scores":       langfuse.SummarizeRankScores(modelRows, 50),
		"composite_results":  langfuse.SummarizeSearchResults(composite, 25),
		"final_results":      langfuse.SummarizeSearchResults(final, 25),
	}
	if len(modelScores) > 50 {
		out["model_scores_truncated"] = len(modelScores) - 50
	}
	return out
}

// rerank performs the actual reranking operation with given query and passages
func (p *PluginRerank) rerank(ctx context.Context,
	chatManage *types.ChatManage, rerankModel rerank.Reranker, query string, passages []string,
	candidates []*types.SearchResult,
) ([]rerank.RankResult, error) {
	pipelineInfo(ctx, "Rerank", "model_call", map[string]interface{}{
		"query_variant": query,
		"passages":      len(passages),
	})

	// Filter out empty or whitespace-only passages before sending to the API
	var cleanPassages []string
	var cleanCandidates []*types.SearchResult
	for i, p := range passages {
		if strings.TrimSpace(p) != "" {
			cleanPassages = append(cleanPassages, p)
			if i < len(candidates) {
				cleanCandidates = append(cleanCandidates, candidates[i])
			}
		}
	}
	if len(cleanPassages) == 0 {
		pipelineInfo(ctx, "Rerank", "model_call_skip", map[string]interface{}{
			"reason": "all_passages_empty",
		})
		return nil, nil
	}
	passages = cleanPassages
	candidates = cleanCandidates

	rerankResp, err := rerankModel.Rerank(ctx, query, passages)
	if err != nil {
		pipelineError(ctx, "Rerank", "model_call", map[string]interface{}{
			"query_variant": query,
			"error":         err.Error(),
		})
		return nil, err
	}

	// Log top scores for debugging
	pipelineInfo(ctx, "Rerank", "threshold", map[string]interface{}{
		"threshold": chatManage.RerankThreshold,
	})
	logged := min(5, len(rerankResp))
	for i := range logged {
		if rerankResp[i].Index < len(candidates) {
			pipelineInfo(ctx, "Rerank", "top_score", map[string]interface{}{
				"rank":        i + 1,
				"score":       rerankResp[i].RelevanceScore,
				"chunk_id":    candidates[rerankResp[i].Index].ID,
				"match_type":  candidates[rerankResp[i].Index].MatchType,
				"chunk_type":  candidates[rerankResp[i].Index].ChunkType,
				"content_len": len(candidates[rerankResp[i].Index].Content),
			})
		}
	}
	if len(rerankResp) > logged {
		pipelineInfo(ctx, "Rerank", "top_score_summary", map[string]interface{}{
			"total":     len(rerankResp),
			"logged":    logged,
			"truncated": len(rerankResp) - logged,
		})
	}

	// Filter results based on threshold
	rankFilter := []rerank.RankResult{}
	for _, result := range rerankResp {
		if result.Index >= len(candidates) {
			continue
		}
		if result.RelevanceScore >= chatManage.RerankThreshold {
			rankFilter = append(rankFilter, result)
		}
	}

	// Fallback: if threshold filtering removed all results but the top candidate
	// still has a reasonable score, keep it as a safety net. Skip fallback entirely
	// when the best score is too low — forcing irrelevant results is worse than
	// returning nothing and letting the caller handle the empty-result case.
	fallbackMinScore := rerankFallbackMinScore(chatManage.SearchTargets)
	if len(rankFilter) == 0 && len(rerankResp) > 0 && rerankResp[0].RelevanceScore >= fallbackMinScore {
		rankFilter = rerankResp[:1]
		pipelineInfo(ctx, "Rerank", "fallback_top1", map[string]interface{}{
			"reason":    "all_below_threshold",
			"threshold": chatManage.RerankThreshold,
			"top_score": rerankResp[0].RelevanceScore,
		})
	} else if len(rankFilter) == 0 {
		pipelineInfo(ctx, "Rerank", "fallback_skip", map[string]interface{}{
			"reason":    "top_score_too_low",
			"threshold": chatManage.RerankThreshold,
			"top_score": safeTopScore(rerankResp),
		})
	}

	return rankFilter, nil
}

func rerankFallbackMinScore(searchTargets types.SearchTargets) float64 {
	if searchTargets.HasRecallThresholdOverride() {
		// The user explicitly constrained this turn to a tag/document scope.
		// Preserve its best candidate instead of letting a global rerank
		// threshold erase the entire authoritative scope.
		return 0
	}
	return 0.15
}

// ensureMetadata ensures the metadata is not nil
func ensureMetadata(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string)
	}
	return m
}

func safeTopScore(results []rerank.RankResult) float64 {
	if len(results) == 0 {
		return 0
	}
	return results[0].RelevanceScore
}

// legacyRankingResolved is the resolved ranking with every field at its
// legacy default — the exact constants the pre-weighted-ranking code used.
var legacyRankingResolved = (&types.RankingConfig{}).Resolve()

// compositeScore calculates the composite score for a search result.
// Delegates to the shared searchutil kernel (upstream U-03) with the legacy
// defaults; bit-identical to the historical 0.6/0.3/0.1 formula.
func compositeScore(sr *types.SearchResult, modelScore, baseScore float64) float64 {
	sourceWeight := 1.0
	switch strings.ToLower(sr.KnowledgeSource) {
	case "web_search":
		sourceWeight = 0.95
	default:
		sourceWeight = 1.0
	}
	return searchutil.CompositeScore(modelScore, baseScore, sourceWeight, 0, legacyRankingResolved)
}

// weightedCompositeScore generalizes compositeScore with configurable weights.
// With the default RankingConfig it reproduces the legacy formula exactly
// (0.6*model + 0.3*base + 0.1*sourceWeight, plus an inert recency term of 0).
func weightedCompositeScore(sr *types.SearchResult, modelScore, baseScore, recency float64, r types.ResolvedRanking) float64 {
	sourceWeight := 1.0
	if strings.ToLower(sr.KnowledgeSource) == "web_search" {
		sourceWeight = 0.95
	}
	return searchutil.CompositeScore(modelScore, baseScore, sourceWeight, recency, r)
}

// recencySignal maps a document's time axis to an exponential decay signal
// in [0,1]: a document dated now scores 1, one half-life ago scores 0.5, and
// so on. Missing or unparsable timestamps yield 0, which is only meaningful
// when RecencyWeight > 0. Time-aware retrieval (设计 §4.5): the document's
// business_date is the primary axis when present, updated_at the fallback —
// the signal itself (RecencySignal) is unchanged, only its input is upgraded.
func recencySignal(sr *types.SearchResult, halfLifeDays int, now time.Time) float64 {
	if sr.Metadata == nil {
		return 0
	}
	// A parseable business_date wins even when its decayed signal is ~0 —
	// a 2020 rating being "old" is the business truth; only unparseable or
	// missing values may fall back to the system timestamp.
	if ts := sr.Metadata["knowledge_business_date"]; ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil && !t.IsZero() {
			return searchutil.RecencySignal(ts, halfLifeDays, now)
		}
	}
	return searchutil.RecencySignal(sr.Metadata["knowledge_updated_at"], halfLifeDays, now)
}

// logSignalDistribution emits one compact summary per rerank round covering
// every signal that feeds the composite score, so weight tuning can be driven
// by observed spread instead of guesswork.
func logSignalDistribution(ctx context.Context, results []*types.SearchResult) {
	if len(results) == 0 {
		return
	}
	pick := func(key string) []float64 {
		vals := make([]float64, 0, len(results))
		for _, r := range results {
			if r.Metadata != nil {
				if v, err := strconv.ParseFloat(r.Metadata[key], 64); err == nil {
					vals = append(vals, v)
				}
			}
		}
		sort.Float64s(vals)
		return vals
	}
	stat := func(vals []float64) string {
		if len(vals) == 0 {
			return "n/a"
		}
		return fmt.Sprintf("min=%.3f p50=%.3f p90=%.3f",
			vals[0], vals[len(vals)/2], vals[min(9*len(vals)/10, len(vals)-1)])
	}
	pipelineInfo(ctx, "Rerank", "signal_distribution", map[string]interface{}{
		"n":       len(results),
		"model":   stat(pick("model_score")),
		"base":    stat(pick("base_score")),
		"recency": stat(pick("recency")),
	})
}

// applyMMR applies the MMR algorithm to the search results. Selection runs
// in the shared searchutil.SelectMMR kernel (upstream U-03); the incremental
// form there produces the same selection order — tie-breaking included — as
// the naive pairwise loop this used to inline, at one comparison per
// remaining candidate per round.
func applyMMR(
	ctx context.Context,
	results []*types.SearchResult,
	chatManage *types.ChatManage,
	k int,
	lambda float64,
) []*types.SearchResult {
	if k <= 0 || len(results) == 0 {
		return nil
	}
	pipelineInfo(ctx, "Rerank", "mmr_start", map[string]interface{}{
		"lambda":     lambda,
		"k":          k,
		"candidates": len(results),
	})

	// Pre-compute all token sets concurrently (CPU-bound tokenization)
	allTokenSets := ParallelMap(results, 0, func(i int, r *types.SearchResult) map[string]struct{} {
		return searchutil.TokenizeSimple(getEnrichedPassage(ctx, r))
	})
	scores := make([]float64, len(results))
	for i, r := range results {
		scores[i] = r.Score
	}

	selectedIndices := searchutil.SelectMMR(k, lambda, scores, allTokenSets)
	selected := make([]*types.SearchResult, 0, len(selectedIndices))
	for _, i := range selectedIndices {
		selected = append(selected, results[i])
	}

	// Compute average redundancy among selected using pre-computed token sets
	avgRed := 0.0
	if len(selectedIndices) > 1 {
		pairs := 0
		for i := 0; i < len(selectedIndices); i++ {
			for j := i + 1; j < len(selectedIndices); j++ {
				avgRed += searchutil.Jaccard(allTokenSets[selectedIndices[i]], allTokenSets[selectedIndices[j]])
				pairs++
			}
		}
		if pairs > 0 {
			avgRed /= float64(pairs)
		}
	}
	pipelineInfo(ctx, "Rerank", "mmr_done", map[string]interface{}{
		"selected":       len(selected),
		"avg_redundancy": fmt.Sprintf("%.4f", avgRed),
	})
	return selected
}

// --- Passage cleaning for rerank ---
//
// Rerank models work on semantic text similarity. Markdown formatting, raw URLs,
// image references, table separators, and other structural syntax are noise that
// can dilute the semantic signal. The functions below strip this noise before
// passages are sent to the rerank model.

var (
	// reMarkdownImage matches ![alt](url) — the entire construct is noise.
	// URL group supports one level of balanced parentheses.
	reMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\([^()\s]*(?:\([^)]*\)[^()\s]*)*\)`)
	// reLinkedImage matches [![alt](img_url)](link_url) — unwrap to ![alt](img_url)
	// so that the subsequent reMarkdownImage pass can remove the image.
	reLinkedImage = regexp.MustCompile(
		`\[!\[([^\]]*)\]\(([^()\s]*(?:\([^)]*\)[^()\s]*)*)\)\]` +
			`\([^()\s]*(?:\([^)]*\)[^()\s]*)*\)`,
	)
	// reMarkdownLink matches [text](url) — we keep the text, drop the URL.
	// URL group supports one level of balanced parentheses.
	reMarkdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^()\s]*(?:\([^)]*\)[^()\s]*)*\)`)
	// reRawURL matches standalone http(s) URLs.
	reRawURL = regexp.MustCompile(`https?://[^\s)\]>]+`)
	// reCodeBlock captures the semantic body of fenced code blocks.
	reCodeBlock = regexp.MustCompile("(?s)```[^\\r\\n]*\\r?\\n(.*?)\\r?\\n?```")
	// reLatexBlock captures the semantic body of block-level LaTeX ($$...$$).
	reLatexBlock = regexp.MustCompile(`(?s)\$\$(.*?)\$\$`)
	// reTableSep matches table separator rows like |---|---|.
	// Uses [ \t] instead of \s to avoid consuming newlines across rows.
	reTableSep = regexp.MustCompile(`(?m)^[ \t]*\|[ \t:|-]+\|[ \t]*$`)
	// reTableRow matches markdown table data rows like | col1 | col2 |.
	// Uses [ \t] instead of \s to avoid consuming newlines across rows.
	reTableRow = regexp.MustCompile(`(?m)^[ \t]*\|(.+?)\|[ \t]*$`)
	// reHeadingPrefix matches leading # markers in headings.
	reHeadingPrefix = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	// reBlockquote matches leading > markers.
	reBlockquote = regexp.MustCompile(`(?m)^>\s?`)
	// reBoldItalic3 matches ***text*** wrappers (must come before 2 and 1).
	reBoldItalic3 = regexp.MustCompile(`\*{3}(.+?)\*{3}`)
	// reBoldItalic2 matches **text** wrappers.
	reBoldItalic2 = regexp.MustCompile(`\*{2}(.+?)\*{2}`)
	// reBoldItalic1 matches *text* wrappers.
	reBoldItalic1 = regexp.MustCompile(`\*(.+?)\*`)
	// reExcessiveNewlines collapses 3+ consecutive newlines into 2.
	reExcessiveNewlines = regexp.MustCompile(`\n{3,}`)
	// reListMarker matches unordered (- , * ) and ordered (1. ) list prefixes.
	reListMarker = regexp.MustCompile(`(?m)^[\t ]*(?:[-*+]|\d+\.)\s+`)
	// reHTMLTag matches HTML tags like <br>, <div class="...">, etc.
	reHTMLTag = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
)

// cleanPassageForRerank strips markdown/structural noise from text to produce
// a clean semantic passage for the rerank model. The cleaning is designed to
// preserve all meaningful semantic content while removing formatting
// that would confuse text-similarity scoring.
func cleanPassageForRerank(text string) string {
	// 1. Unwrap code blocks so code-only candidates remain rerankable.
	text = reCodeBlock.ReplaceAllString(text, "$1")
	// 2. Unwrap LaTeX blocks so formula-only candidates remain rerankable.
	text = reLatexBlock.ReplaceAllString(text, "$1")
	// 3. Remove HTML tags
	text = reHTMLTag.ReplaceAllString(text, "")
	// 3.5. Unwrap nested [![alt](img_url)](link_url) → ![alt](img_url)
	//      so that the next step removes the full construct cleanly.
	text = reLinkedImage.ReplaceAllString(text, "![$1]($2)")
	// 4. Remove markdown image references entirely
	text = reMarkdownImage.ReplaceAllString(text, "")
	// 5. Convert markdown links to just their display text
	text = reMarkdownLink.ReplaceAllString(text, "$1")
	// 6. Remove standalone raw URLs
	text = reRawURL.ReplaceAllString(text, "")
	// 7. Remove table separator rows
	text = reTableSep.ReplaceAllString(text, "")
	// 7.5. Convert table data rows to plain text (strip | delimiters)
	text = reTableRow.ReplaceAllStringFunc(text, func(match string) string {
		inner := reTableRow.FindStringSubmatch(match)
		if len(inner) < 2 {
			return match
		}
		cells := strings.Split(inner[1], "|")
		var parts []string
		for _, cell := range cells {
			cell = strings.TrimSpace(cell)
			if cell != "" {
				parts = append(parts, cell)
			}
		}
		return strings.Join(parts, ", ")
	})
	// 8. Strip heading markers but keep heading text
	text = reHeadingPrefix.ReplaceAllString(text, "")
	// 9. Strip blockquote markers
	text = reBlockquote.ReplaceAllString(text, "")
	// 10. Unwrap bold/italic markers, keeping inner text (order: *** before ** before *)
	text = reBoldItalic3.ReplaceAllString(text, "$1")
	text = reBoldItalic2.ReplaceAllString(text, "$1")
	text = reBoldItalic1.ReplaceAllString(text, "$1")
	// 11. Strip list markers
	text = reListMarker.ReplaceAllString(text, "")
	// 12. Collapse excessive newlines
	text = reExcessiveNewlines.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

// getEnrichedPassage 合并Content、ImageInfo和GeneratedQuestions的文本内容
func getEnrichedPassage(ctx context.Context, result *types.SearchResult) string {
	combinedText := cleanPassageForRerank(result.Content)
	var enrichments []string

	// 解析ImageInfo
	if result.ImageInfo != "" {
		var imageInfos []types.ImageInfo
		err := json.Unmarshal([]byte(result.ImageInfo), &imageInfos)
		if err != nil {
			pipelineWarn(ctx, "Rerank", "image_info_parse", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			// 提取所有图片的描述和OCR文本
			for _, img := range imageInfos {
				if img.Caption != "" {
					enrichments = append(enrichments, img.Caption)
				}
				if img.OCRText != "" {
					enrichments = append(enrichments, img.OCRText)
				}
			}
		}
	}

	// 解析ChunkMetadata中的GeneratedQuestions
	if len(result.ChunkMetadata) > 0 {
		var docMeta types.DocumentChunkMetadata
		err := json.Unmarshal(result.ChunkMetadata, &docMeta)
		if err != nil {
			pipelineWarn(ctx, "Rerank", "chunk_metadata_parse", map[string]interface{}{
				"error": err.Error(),
			})
		} else if questionStrings := docMeta.GetQuestionStrings(); len(questionStrings) > 0 {
			enrichments = append(enrichments, strings.Join(questionStrings, "; "))
		}
	}

	if len(enrichments) == 0 {
		return combinedText
	}

	// 组合内容和增强信息
	if combinedText != "" {
		combinedText += "\n\n"
	}
	combinedText += strings.Join(enrichments, "\n")

	return combinedText
}

func logRerankInputScoreSample(ctx context.Context, results []*types.SearchResult) {
	const maxLogRows = 8
	limit := min(maxLogRows, len(results))
	for i := 0; i < limit; i++ {
		sr := results[i]
		pipelineInfo(ctx, "Rerank", "input_score", map[string]interface{}{
			"index":      i,
			"chunk_id":   sr.ID,
			"score":      fmt.Sprintf("%.4f", sr.Score),
			"match_type": sr.MatchType,
		})
	}
	if len(results) > limit {
		pipelineInfo(ctx, "Rerank", "input_score_summary", map[string]interface{}{
			"total":     len(results),
			"logged":    limit,
			"truncated": len(results) - limit,
		})
	}
}
