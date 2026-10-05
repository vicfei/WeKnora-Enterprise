package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
)

// kb-routing v2（台账 T10，口径：LLM 路由 + 字典冷启动）。
//
// v1 是内容质心相似度路由（kb_router.go）：冷缓存首问要为全部候选库批量算嵌入
// （541 库实测 36-38s，还引发过 429），且跨嵌入模型的库不可比。v2 把决策层换成
// LLM 读库目录（名称+描述），字典（名称/描述的分词索引）只做两件事：命中最强时
// 直接短路（冷启动零延迟路径），以及超大候选集时为 LLM 预筛目录。质心层保留为
// LLM 不可用时的回退。决策方法记录在 KBRoutingInfo.Method（llm / dict / centroid）。

const (
	kbRouterLLMEnabledEnv     = "KB_ROUTER_LLM" // "0"/"false" 关闭 v2 层
	kbRouterLLMModelEnv       = "KB_ROUTER_LLM_MODEL_ID"
	kbRouterLLMCatalogEnv     = "KB_ROUTER_LLM_MAX_CATALOG"
	kbRouterLLMDefaultCatalog = 40
	kbRouterLLMMaxTokens      = 400
	kbRouterLLMTimeout        = 15 * time.Second
	kbRouterLLMMaxSelection   = 5
	// 字典短路阈值：查询分词被库文本覆盖 ≥0.6 且领先第二名 1.5 倍，才认定
	// 单库命中足够强，跳过 LLM。其余情况字典只用于排序目录。
	kbRouterDictStrongCoverage = 0.6
	kbRouterDictStrongLead     = 1.5
)

func kbRouterLLMEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(kbRouterLLMEnabledEnv))) {
	case "0", "false", "off":
		return false
	default:
		return true
	}
}

func kbRouterLLMCatalogCap() int {
	if raw := strings.TrimSpace(os.Getenv(kbRouterLLMCatalogEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return kbRouterLLMDefaultCatalog
}

// dictScoreKBs scores every candidate by query-token coverage of its
// name+description lexicon (the cold-start dictionary). Coverage of 1.0 means
// every query token appears in the KB's text; the score is coverage, not a
// similarity, so short names are not penalised. Sorted best first.
func dictScoreKBs(candidates []*types.KnowledgeBase, query string) []KBScore {
	qTokens := searchutil.TokenizeSimple(query)
	if len(qTokens) == 0 {
		return nil
	}
	scores := make([]KBScore, 0, len(candidates))
	for _, kb := range candidates {
		if kb == nil {
			continue
		}
		kbTokens := searchutil.TokenizeSimple(kb.Name + " " + kb.Description)
		if len(kbTokens) == 0 {
			scores = append(scores, KBScore{KBID: kb.ID, Name: kb.Name, Score: 0})
			continue
		}
		matched := 0
		for t := range qTokens {
			if _, ok := kbTokens[t]; ok {
				matched++
			}
		}
		scores = append(scores, KBScore{
			KBID:  kb.ID,
			Name:  kb.Name,
			Score: float64(matched) / float64(len(qTokens)),
		})
	}
	sort.SliceStable(scores, func(a, b int) bool {
		if scores[a].Score != scores[b].Score {
			return scores[a].Score > scores[b].Score
		}
		return scores[a].KBID < scores[b].KBID
	})
	return scores
}

// llmRouteKnowledgeBases is the v2 decision layer. Returns (ids, info) with
// info.Method set to "dict" or "llm" on a decision; (nil, nil) when the layer
// is disabled or cannot run (no model, call failure, unparsable output) — the
// caller then falls back to the centroid router.
func (s *sessionService) llmRouteKnowledgeBases(
	ctx context.Context, candidates []*types.KnowledgeBase, query string,
) ([]string, *KBRoutingInfo) {
	if !kbRouterLLMEnabled() {
		return nil, nil
	}
	dict := dictScoreKBs(candidates, query)

	// Cold-start fast path: one dominant dictionary hit answers instantly,
	// no model call, no embedding — the first question stays fast.
	if len(dict) > 0 && dict[0].Score >= kbRouterDictStrongCoverage {
		lead := dict[0].Score
		second := 0.0
		if len(dict) > 1 {
			second = dict[1].Score
		}
		if second == 0 || lead >= second*kbRouterDictStrongLead {
			return []string{dict[0].KBID}, &KBRoutingInfo{
				Selected: dict[:1],
				Method:   "dict",
				Reason:   fmt.Sprintf("dict coverage %.2f (lead %.2f)", lead, second),
			}
		}
	}

	// Catalog: dict-ranked (all of them when under the cap) keeps the prompt
	// small and the strongest name matches visible to the model.
	capN := kbRouterLLMCatalogCap()
	if capN > len(candidates) {
		capN = len(candidates)
	}
	byID := map[string]*types.KnowledgeBase{}
	for _, kb := range candidates {
		if kb != nil {
			byID[kb.ID] = kb
		}
	}
	catalog := make([]*types.KnowledgeBase, 0, capN)
	seen := map[string]bool{}
	for _, sc := range dict {
		if len(catalog) >= capN {
			break
		}
		if kb := byID[sc.KBID]; kb != nil && !seen[sc.KBID] {
			catalog = append(catalog, kb)
			seen[sc.KBID] = true
		}
	}
	for _, kb := range candidates {
		if len(catalog) >= capN {
			break
		}
		if kb != nil && !seen[kb.ID] {
			catalog = append(catalog, kb)
			seen[kb.ID] = true
		}
	}
	if len(catalog) == 0 {
		return nil, nil
	}

	model, err := s.kbRouterLLMModel(ctx)
	if err != nil || model == nil {
		logger.Warnf(ctx, "kb_router: llm layer unavailable (%v), falling back to centroid", err)
		return nil, nil
	}

	var b strings.Builder
	b.WriteString("编号  知识库（名称 — 描述）\n")
	for i, kb := range catalog {
		desc := strings.TrimSpace(kb.Description)
		if r := []rune(desc); len(r) > 100 {
			desc = string(r[:100]) + "…"
		}
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, strings.TrimSpace(kb.Name), desc)
	}
	system := "你是知识库路由器。给定用户问题和候选知识库目录，选出回答该问题需要检索的知识库编号。\n" +
		"规则：只选内容与问题直接相关的库；最多选 " + strconv.Itoa(kbRouterLLMMaxSelection) + " 个；\n" +
		"按相关性从高到低排列；没有任何相关的库时返回空数组。只输出 JSON，不要解释：\n" +
		`{"kb_indexes": [1, 3]}`
	user := "候选知识库目录：\n" + b.String() + "\n用户问题：" + query

	llmCtx, cancel := context.WithTimeout(ctx, kbRouterLLMTimeout)
	defer cancel()
	llmCtx = types.WithLLMCallMetadata(llmCtx, "kb_router", "")
	thinking := false
	resp, err := model.Chat(llmCtx, []chat.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &chat.ChatOptions{
		Temperature:         0,
		MaxCompletionTokens: kbRouterLLMMaxTokens,
		Thinking:            &thinking,
	})
	if err != nil {
		logger.Warnf(ctx, "kb_router: llm call failed (%v), falling back to centroid", err)
		return nil, nil
	}

	indexes := parseKBRouterSelection(resp.Content)
	if indexes == nil {
		logger.Warnf(ctx, "kb_router: llm output unparsable (%.80s), falling back to centroid", resp.Content)
		return nil, nil
	}
	info := &KBRoutingInfo{Method: "llm"}
	ids := make([]string, 0, len(indexes))
	added := map[string]bool{}
	for _, idx := range indexes {
		if idx < 1 || idx > len(catalog) {
			continue
		}
		kb := catalog[idx-1]
		if added[kb.ID] {
			continue
		}
		added[kb.ID] = true
		ids = append(ids, kb.ID)
		info.Selected = append(info.Selected, KBScore{KBID: kb.ID, Name: kb.Name})
	}
	if len(ids) == 0 {
		info.Reason = "llm_no_match"
	}
	info.Skipped = len(candidates) - len(catalog)
	logger.Infof(ctx, "kb_router: llm selected %d/%d (catalog %d, skipped %d)",
		len(ids), len(candidates), len(catalog), info.Skipped)
	return ids, info
}

// kbRouterLLMModel resolves the router's chat model: explicit override first,
// else the tenant's first KnowledgeQA model (the rerank-"auto" precedent).
func (s *sessionService) kbRouterLLMModel(ctx context.Context) (chat.Chat, error) {
	if id := strings.TrimSpace(os.Getenv(kbRouterLLMModelEnv)); id != "" {
		return s.modelService.GetChatModel(ctx, id)
	}
	models, err := s.modelService.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		if m != nil && m.Type == types.ModelTypeKnowledgeQA {
			return s.modelService.GetChatModel(ctx, m.ID)
		}
	}
	return nil, fmt.Errorf("no KnowledgeQA model available")
}

var kbRouterJSONRe = regexp.MustCompile(`\{[^{}]*\}`)

// parseKBRouterSelection extracts {"kb_indexes":[...]} from a model reply,
// tolerating surrounding prose. Returns nil when nothing parseable is found
// (an honest "no decision" — the caller falls back).
func parseKBRouterSelection(raw string) []int {
	for _, m := range kbRouterJSONRe.FindAllString(raw, -1) {
		var out struct {
			KBIndexes []int `json:"kb_indexes"`
		}
		if err := json.Unmarshal([]byte(m), &out); err == nil && out.KBIndexes != nil {
			return out.KBIndexes
		}
	}
	return nil
}
