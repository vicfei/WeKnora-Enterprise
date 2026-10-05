package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// kb_router.go — 二开特性：知识库路由 v1（feature/kb-routing）。
//
// 当请求未显式选择知识库时，从租户全部可见库中自动挑出最相关的 top-K：
// 每个库缓存其文档 digest 的 embedding（v1.1：按"问题 vs 各文档最大相似度"
// 打分，质心均值会被干扰段落稀释导致错路由——v1 实测金标库输给话题相近库）。显式传 knowledge_base_ids 的请求不经过路由，
// 行为不变；路由仅由请求级 enable_kb_routing 开关触发（默认关闭）。
//
// v1 已知边界（后续主题迭代）：
//   - 质心按 TTL 失效（默认 15 分钟），文档增删不会立刻反映；
//   - 跨 embedding 模型的库只按多数模型分组参与打分，其余跳过并记日志；
//   - 相似度对"多主题混合库"偏保守（质心被稀释），v2 计划换多簇表示。

const (
	kbRouterDefaultTopK      = 3
	kbRouterDefaultMinScore  = 0.25
	kbRouterDefaultCacheTTL  = 15 * time.Minute
	kbRouterDigestMaxRunes   = 400
	kbRouterMaxCandidateKBs  = 2000
	kbRouterEmbedConcurrency = 8
	kbRouterEventToolName    = "kb_router"
	kbRouterCentroidCacheEnv = "KB_ROUTER_CACHE_TTL_MIN"
	kbRouterTopKEnv          = "KB_ROUTER_TOP_K"
	kbRouterMinScoreEnv      = "KB_ROUTER_MIN_SCORE"
)

// KBScore is one candidate's routing score.
type KBScore struct {
	KBID  string  `json:"kb_id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// KBRoutingInfo carries the routing decision for observability (SSE tool_call
// event + logs). Attached to PipelineRequest so it survives into the pipeline.
type KBRoutingInfo struct {
	Selected []KBScore `json:"selected"`
	Skipped  int       `json:"skipped_candidates"`
	Reason   string    `json:"reason,omitempty"`
	// Method records which layer decided: "dict" (cold-start dictionary
	// short-circuit), "llm" (v2 catalog selection) or "centroid" (v1
	// embedding fallback). Empty on v1 failure paths.
	Method string `json:"method,omitempty"`
}

type kbCentroidCacheEntry struct {
	vectors   [][]float32 // one embedding per document digest
	modelID   string
	docCount  int
	expiresAt time.Time
}

var (
	kbCentroidCache   = map[string]kbCentroidCacheEntry{}
	kbCentroidCacheMu sync.RWMutex
)

func kbRouterCacheTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(kbRouterCentroidCacheEnv)); raw != "" {
		if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return kbRouterDefaultCacheTTL
}

func kbRouterTopK() int {
	if raw := strings.TrimSpace(os.Getenv(kbRouterTopKEnv)); raw != "" {
		if k, err := strconv.Atoi(raw); err == nil && k > 0 {
			return k
		}
	}
	return kbRouterDefaultTopK
}

func kbRouterMinScore() float64 {
	if raw := strings.TrimSpace(os.Getenv(kbRouterMinScoreEnv)); raw != "" {
		if s, err := strconv.ParseFloat(raw, 64); err == nil {
			return s
		}
	}
	return kbRouterDefaultMinScore
}

// routeKnowledgeBases selects the most relevant KBs for a query from the
// tenant's visible KBs. Returns the selected KB ids (possibly empty on
// failure — the caller then behaves as before, i.e. no KB targets) plus the
// scoring details for observability.
//
// ⚠️ v1 candidates are ALL tenant KBs (ListKnowledgeBasesByTenantID), not
// per-user visible KBs. Acceptable for the eval tenant (single user) but a
// privacy gap for production: routing a user into a colleague's private KB
// would leak it. Before enabling routing outside eval, filter candidates by
// the requesting user's effective KB visibility (same source as the portal
// list endpoint). Tracked in docs/design/four-track-roadmap.md.
func (s *sessionService) routeKnowledgeBases(
	ctx context.Context, tenantID uint64, query string,
) ([]string, *KBRoutingInfo) {
	candidates, err := s.knowledgeBaseService.ListKnowledgeBasesByTenantID(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "kb_router: list candidates failed: %v", err)
		return nil, &KBRoutingInfo{Reason: "list_failed"}
	}
	if len(candidates) == 0 {
		return nil, &KBRoutingInfo{Reason: "no_candidates"}
	}
	if len(candidates) > kbRouterMaxCandidateKBs {
		logger.Warnf(ctx, "kb_router: %d candidates exceeds cap %d, refusing to route",
			len(candidates), kbRouterMaxCandidateKBs)
		return nil, &KBRoutingInfo{Reason: "too_many_candidates", Skipped: len(candidates)}
	}

	// v2 决策层（kb_router_llm.go，台账 T10）：LLM 读目录选库，字典冷启动短路，
	// 质心相似度退为 LLM 不可用时的回退。返回 (nil, nil) 即下落质心路径。
	if ids, info := s.llmRouteKnowledgeBases(ctx, candidates, query); info != nil {
		return ids, info
	}

	// Majority embedding-model group: vectors are only comparable within one
	// model, so KBs on minority models are skipped (logged).
	modelCount := map[string]int{}
	for _, kb := range candidates {
		modelCount[kb.EmbeddingModelID]++
	}
	majorModel, majority := "", 0
	for m, n := range modelCount {
		if n > majority {
			majorModel, majority = m, n
		}
	}
	pool := candidates[:0]
	for _, kb := range candidates {
		if kb.EmbeddingModelID == majorModel {
			pool = append(pool, kb)
		}
	}
	skipped := len(candidates) - len(pool)

	// Ensure centroids (cached; first call pays one BatchEmbed per KB).
	var wg sync.WaitGroup
	sem := make(chan struct{}, kbRouterEmbedConcurrency)
	for _, kb := range pool {
		if s.kbCentroidCached(kb.ID, kb.EmbeddingModelID) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(kb *types.KnowledgeBase) {
			defer wg.Done()
			defer func() { <-sem }()
			s.computeKBCentroid(ctx, kb)
		}(kb)
	}
	wg.Wait()

	// Query vector via the majority model (resolved through the first pool KB).
	var queryVec []float32
	for _, kb := range pool {
		if v, err := s.knowledgeBaseService.GetQueryEmbedding(ctx, kb.ID, query); err == nil && len(v) > 0 {
			queryVec = v
			break
		}
	}
	if queryVec == nil {
		logger.Warnf(ctx, "kb_router: query embedding failed for model %s", majorModel)
		return nil, &KBRoutingInfo{Reason: "query_embed_failed", Skipped: skipped}
	}

	scores := make([]KBScore, 0, len(pool))
	kbCentroidCacheMu.RLock()
	for _, kb := range pool {
		entry, ok := kbCentroidCache[kb.ID]
		if !ok || entry.modelID != kb.EmbeddingModelID {
			continue
		}
		// v1.1: score = max doc similarity, not the centroid. A KB's 10
		// paragraphs are mostly distractors — averaging dilutes the gold doc
		// to 1/10 weight and mis-routes (measured on the eval: gold KB lost
		// to topical neighbours). Max-doc is a vector probe per KB.
		best := -1.0
		for _, v := range entry.vectors {
			if len(v) != len(queryVec) {
				continue
			}
			if s := cosineSimilarity(queryVec, v); s > best {
				best = s
			}
		}
		if best > -1.0 {
			scores = append(scores, KBScore{KBID: kb.ID, Name: kb.Name, Score: best})
		}
	}
	kbCentroidCacheMu.RUnlock()

	if len(scores) == 0 {
		return nil, &KBRoutingInfo{Reason: "no_valid_centroids", Skipped: skipped}
	}
	sortKBScoresDesc(scores)

	topK := kbRouterTopK()
	minScore := kbRouterMinScore()
	selected := make([]KBScore, 0, topK)
	ids := make([]string, 0, topK)
	for _, sc := range scores {
		if len(selected) >= topK {
			break
		}
		if sc.Score < minScore && len(selected) > 0 {
			break // keep the top-1 even below threshold: routing beats failing
		}
		selected = append(selected, sc)
		ids = append(ids, sc.KBID)
	}

	info := &KBRoutingInfo{Selected: selected, Skipped: skipped, Method: "centroid"}
	logger.Infof(ctx, "kb_router: query routed to %d/%d KBs (top score %.3f)",
		len(ids), len(candidates), scores[0].Score)
	return ids, info
}

func (s *sessionService) kbCentroidCached(kbID, modelID string) bool {
	kbCentroidCacheMu.RLock()
	defer kbCentroidCacheMu.RUnlock()
	e, ok := kbCentroidCache[kbID]
	return ok && e.modelID == modelID && time.Now().Before(e.expiresAt)
}

// computeKBCentroid embeds the KB's document digests and stores their
// normalized mean. Digest = title + content head, so a KB's identity comes
// from what it actually contains, not from its (often generic) description.
func (s *sessionService) computeKBCentroid(ctx context.Context, kb *types.KnowledgeBase) {
	docs, err := s.knowledgeService.ListKnowledgeByKnowledgeBaseID(ctx, kb.ID)
	if err != nil {
		logger.Warnf(ctx, "kb_router: list docs for %s failed: %v", kb.ID, err)
		return
	}
	if len(docs) == 0 {
		return
	}
	texts := make([]string, 0, len(docs))
	for _, d := range docs {
		texts = append(texts, docDigest(d))
	}
	embedder, err := s.modelService.GetEmbeddingModelForTenant(ctx, kb.EmbeddingModelID, kb.TenantID)
	if err != nil {
		logger.Warnf(ctx, "kb_router: embedder for %s unavailable: %v", kb.ID, err)
		return
	}
	vecs, err := embedder.BatchEmbed(ctx, texts)
	if err != nil || len(vecs) != len(texts) {
		logger.Warnf(ctx, "kb_router: embed docs of %s failed: %v", kb.ID, err)
		return
	}
	kbCentroidCacheMu.Lock()
	kbCentroidCache[kb.ID] = kbCentroidCacheEntry{
		vectors:   vecs,
		modelID:   kb.EmbeddingModelID,
		docCount:  len(docs),
		expiresAt: time.Now().Add(kbRouterCacheTTL()),
	}
	kbCentroidCacheMu.Unlock()
}

// docDigest builds the text a KB is represented by. Manual docs contribute
// their stored content; file-based docs contribute title+description+file
// name (their text lives in chunks, which v1 deliberately does not read to
// keep centroid computation one metadata query + one embedding batch).
func docDigest(d *types.Knowledge) string {
	var b strings.Builder
	b.WriteString(d.Title)
	if d.Description != "" {
		b.WriteString("\n")
		b.WriteString(d.Description)
	}
	if d.FileName != "" {
		b.WriteString("\n")
		b.WriteString(d.FileName)
	}
	if meta, err := d.ManualMetadata(); err == nil && meta != nil && meta.Content != "" {
		b.WriteString("\n")
		head := []rune(meta.Content)
		if len(head) > kbRouterDigestMaxRunes {
			head = head[:kbRouterDigestMaxRunes]
		}
		b.WriteString(string(head))
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = d.Title
	}
	return out
}

// foldBestPerKB keeps only each KB's best score (max-doc scoring emits one
// entry per doc; without the fold-down a single KB's runner-up docs consumed
// top-K slots and starved the other candidates).
func foldBestPerKB(scores []KBScore) []KBScore {
	best := make(map[string]KBScore, len(scores))
	for _, sc := range scores {
		if cur, ok := best[sc.KBID]; !ok || sc.Score > cur.Score {
			best[sc.KBID] = sc
		}
	}
	out := make([]KBScore, 0, len(best))
	for _, sc := range best {
		out = append(out, sc)
	}
	return out
}

func sortKBScoresDesc(scores []KBScore) {
	for i := 1; i < len(scores); i++ {
		for j := i; j > 0 && scores[j].Score > scores[j-1].Score; j-- {
			scores[j], scores[j-1] = scores[j-1], scores[j]
		}
	}
}

// emitKBRoutingEvent publishes the routing decision as a kb_router tool_call
// so SSE consumers (and the eval harness) can see which KBs were chosen.
func emitKBRoutingEvent(ctx context.Context, chatManage *types.ChatManage, info *KBRoutingInfo) {
	if chatManage == nil || chatManage.EventBus == nil || info == nil || len(info.Selected) == 0 {
		return
	}
	selected := make([]map[string]any, 0, len(info.Selected))
	for _, sc := range info.Selected {
		selected = append(selected, map[string]any{
			"kb_id": sc.KBID, "name": sc.Name, "score": sc.Score,
		})
	}
	_ = chatManage.EventBus.Emit(ctx, types.Event{
		Type:      types.EventType(event.EventAgentToolCall),
		SessionID: chatManage.SessionID,
		Data: event.AgentToolCallData{
			ToolCallID: chatManage.SessionID + "-kb-router",
			ToolName:   kbRouterEventToolName,
			Arguments: map[string]any{
				"selected":           selected,
				"skipped_candidates": info.Skipped,
			},
		},
	})
}

// emitAgentKBRoutingEvent is the agent-path twin of emitKBRoutingEvent: the
// agent pipeline has no ChatManage, so the bus and session id come in
// directly. The event payload is identical so SSE consumers (frontend, eval
// harness) need no mode-specific parsing.
func emitAgentKBRoutingEvent(ctx context.Context, bus *event.EventBus, sessionID string, info *KBRoutingInfo) {
	if bus == nil || info == nil || len(info.Selected) == 0 {
		return
	}
	selected := make([]map[string]any, 0, len(info.Selected))
	for _, sc := range info.Selected {
		selected = append(selected, map[string]any{
			"kb_id": sc.KBID, "name": sc.Name, "score": sc.Score,
		})
	}
	_ = bus.Emit(ctx, event.Event{
		Type:      event.EventAgentToolCall,
		SessionID: sessionID,
		Data: event.AgentToolCallData{
			ToolCallID: sessionID + "-kb-router",
			ToolName:   kbRouterEventToolName,
			Arguments: map[string]any{
				"selected":           selected,
				"skipped_candidates": info.Skipped,
			},
		},
	})
}
