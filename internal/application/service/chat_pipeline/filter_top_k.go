package chatpipeline

import (
	"context"
	"sort"

	"github.com/Tencent/WeKnora/internal/types"
)

// PluginFilterTopK is a plugin that filters search results to keep only the top K items
type PluginFilterTopK struct{}

// NewPluginFilterTopK creates a new instance of PluginFilterTopK and registers it with the event manager
func NewPluginFilterTopK(eventManager *EventManager) *PluginFilterTopK {
	res := &PluginFilterTopK{}
	eventManager.Register(res)
	return res
}

// ActivationEvents returns the event types that this plugin responds to
func (p *PluginFilterTopK) ActivationEvents() []types.EventType {
	return []types.EventType{types.FILTER_TOP_K}
}

// OnEvent handles the FILTER_TOP_K event by filtering results to keep only the top K items
// It can filter MergeResult, RerankResult, or SearchResult depending on which is available
func (p *PluginFilterTopK) OnEvent(ctx context.Context,
	eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	if !chatManage.NeedsRetrieval() {
		return next()
	}
	pipelineInfo(ctx, "FilterTopK", "input", map[string]interface{}{
		"session_id": chatManage.SessionID,
		"top_k":      chatManage.RerankTopK,
		"merge_cnt":  len(chatManage.MergeResult),
		"rerank_cnt": len(chatManage.RerankResult),
		"search_cnt": len(chatManage.SearchResult),
	})

	filterTopK := func(searchResult []*types.SearchResult, topK int) []*types.SearchResult {
		// P2 修复（WSL2 反馈）：网页搜索结果与 KB chunk 的分数域不可比
		// （composite 后网页 ~0.99，KB ~0.3-0.6），全局排序后网页必然霸占
		// 头部槽位、KB chunk 被挤出截断线。配额制：网页结果在 top-K 中
		// 占比不超过 30%（至少 1 条），KB 结果保底剩余槽位。
		searchResult = capWebSearchResults(ctx, searchResult, topK)
		sortSearchResultsDeterministically(searchResult)
		if topK > 0 && len(searchResult) > topK {
			pipelineInfo(ctx, "FilterTopK", "filter", map[string]interface{}{
				"before": len(searchResult),
				"after":  topK,
			})
			searchResult = searchResult[:topK]
		}
		return searchResult
	}

	if len(chatManage.MergeResult) > 0 {
		chatManage.MergeResult = filterTopK(chatManage.MergeResult, chatManage.RerankTopK)
	} else if len(chatManage.RerankResult) > 0 {
		chatManage.RerankResult = filterTopK(chatManage.RerankResult, chatManage.RerankTopK)
	} else if len(chatManage.SearchResult) > 0 {
		chatManage.SearchResult = filterTopK(chatManage.SearchResult, chatManage.RerankTopK)
	} else {
		pipelineWarn(ctx, "FilterTopK", "skip", map[string]interface{}{
			"reason": "no_results",
		})
	}

	pipelineInfo(ctx, "FilterTopK", "output", map[string]interface{}{
		"merge_cnt":  len(chatManage.MergeResult),
		"rerank_cnt": len(chatManage.RerankResult),
		"search_cnt": len(chatManage.SearchResult),
	})
	return next()
}

// sortSearchResultsDeterministically restores the global relevance order after
// merge stages group results through maps. Stable tie-breakers keep identical
// requests reproducible before TopK truncation.
func sortSearchResultsDeterministically(results []*types.SearchResult) {
	sort.SliceStable(results, func(i, j int) bool {
		left, right := results[i], results[j]
		if left == nil || right == nil {
			return left != nil
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.KnowledgeID != right.KnowledgeID {
			return left.KnowledgeID < right.KnowledgeID
		}
		if left.ChunkType != right.ChunkType {
			return left.ChunkType < right.ChunkType
		}
		if left.ChunkIndex != right.ChunkIndex {
			return left.ChunkIndex < right.ChunkIndex
		}
		return left.ID < right.ID
	})
}

// maxWebSearchQuota is the fraction of top-K slots web search results may
// occupy. Web composite scores (~0.99 after rerank) and KB vector scores
// (~0.3-0.6) live on different scales; without a cap web results displace
// every KB chunk from the ranked list (WSL2 production finding P2).
const maxWebSearchQuota = 0.3

// capWebSearchResults limits web search entries to quota × topK (minimum 1),
// keeping the highest-scoring ones. When no web results are present the
// input is returned unchanged — existing KB-only behaviour is untouched.
func capWebSearchResults(ctx context.Context, results []*types.SearchResult, topK int) []*types.SearchResult {
	if topK <= 0 || len(results) == 0 {
		return results
	}
	maxWeb := max(1, int(float64(topK)*maxWebSearchQuota))
	var web, kb []*types.SearchResult
	webCount := 0
	for _, r := range results {
		if r != nil && r.MatchType == types.MatchTypeWebSearch {
			webCount++
			web = append(web, r)
		} else {
			kb = append(kb, r)
		}
	}
	if webCount <= maxWeb {
		return results
	}
	// Keep the top maxWeb web results by Score.
	sort.SliceStable(web, func(i, j int) bool { return web[i].Score > web[j].Score })
	web = web[:maxWeb]
	pipelineInfo(ctx, "FilterTopK", "web_quota_capped", map[string]interface{}{
		"web_before": webCount,
		"web_after":  maxWeb,
		"kb_count":   len(kb),
	})
	return append(kb, web...)
}
