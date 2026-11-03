# 深度解析：多维度加权排序（Weighted Ranking）二开实现

> 分支 `feature/weighted-ranking`（基线 feature/kb-routing @ c6721e34），核心提交
> 8a77b068 / 5d117c48 / be7c1adb。本文覆盖：WeKnora 架构速览 → 官方实现分析 →
> 二开设计与代码地图 → 对比 → 测试有效性讨论 → 使用方式 → 已知问题。

## 1. 摘要

本特性把检索结果排序从"一组写死的常数"改造成"可配置、可观测、可实验的体系"，包含四件事：

1. **`rerank_model_id` 请求级字段**——修复官方 chat 链路从不接入 rerank 模型的隐藏缺陷（这是全部工作中收益最大的一项）；
2. **`enable_weighted_ranking` + `ranking` 权重对象**——rerank/检索/来源/时效四路权重 + MMR λ + 时效半衰期，请求级 > 租户级 > 旧常量；
3. **时效（recency）信号管线**——文档更新时间的指数衰减分，零额外查询，默认权重 0；
4. **信号分布日志**——每轮一条各信号 min/p50/p90 汇总，落实"先观察分布再定权重"。

设计红线：**开关关闭时与官方行为逐位一致**（单元测试 + 新旧基线实测双重锁死）。

## 2. WeKnora 架构速览（与本特性相关的路径）

```
┌─ 前端 Vue3 + pinia + TDesign（vite 构建，VITE_ENABLE_* 门控二开 UI）────┐
│  Input-field.vue（输入工具栏） → api/chat/streame.ts（请求体白名单）      │
│  stores/settings.ts（开关状态 + 会话快照恢复）                            │
└──────────────┬───────────────────────────────────────────────┬─────────┘
               │ SSE (POST /api/v1/knowledge-chat/:id)          │ GET sessions
┌──────────────▼───────────────────────────────────────────────▼─────────┐
│ handler 层 internal/handler/session/qa.go                                │
│   parseQARequest → CreateKnowledgeQARequest（请求 DTO）→ qaRequestContext│
│   → buildQARequest → types.QARequest；请求后写 SessionLastRequestState   │
├──────────────────────────────────────────────────────────────────────────┤
│ 服务层 internal/application/service/session_knowledge_qa.go              │
│   播种 PipelineRequest（全局 ConversationConfig 值）                      │
│   → agent 覆盖(applyAgentOverrides) → chat 管线触发                       │
├──────────────────────────────────────────────────────────────────────────┤
│ 管线层 chat_pipeline（container.go 注册的插件中间件链，按事件类型分发）    │
│   CHUNK_SEARCH_PARALLEL → CHUNK_RERANK(+WikiBoost) → CHUNK_MERGE         │
│   → FILTER_TOP_K → INTO_CHAT_MESSAGE → CHAT_COMPLETION_STREAM            │
├──────────────────────────────────────────────────────────────────────────┤
│ 检索层 knowledgebase_search.go：向量+关键词双路 → RRF 融合(rrfK=60,       │
│   v/k=0.7/0.3 可配) → processSearchResults 批量富化（含 Knowledge 行）    │
│ 模型层 models/rerank/*：dashscope 等七家 rerank provider                  │
└──────────────────────────────────────────────────────────────────────────┘
```

**配置四层**：config.yaml 全局 → 租户 `RetrievalConfig`（tenants 表 JSONB）→ 智能体
`CustomAgentConfig` → 请求级（官方只支持范围类参数，本特性把排序参数也提上来）。

## 3. 官方实现分析：排序是怎么"写死"的

### 3.1 复合分与 MMR（chat_pipeline/rerank.go）

官方对 rerank 模型返回的每个候选计算：

```go
composite := 0.6*modelScore + 0.3*baseScore + 0.1*sourceWeight  // rerank.go:447
// sourceWeight: web_search=0.95，其余 1.0；clamp [0,1]
final := applyMMR(ctx, reranked, chatManage, k, 0.7)            // λ=0.7 写死
```

- `modelScore`=rerank 模型相关性分；`baseScore`=RRF 融合检索分；权重 0.6/0.3/0.1、λ=0.7、
  阈值兜底 0.15 全部硬编码，**没有任何配置入口**；
- 同一套公式在 `agent/tools/knowledge_search.go:1209` 还有一份拷贝（双路径漂移隐患）。

### 3.2 隐藏缺陷：chat 链路的 rerank 从不生效（upstream issue U-01）

- `PluginRerank` 开场检查 `chatManage.RerankModelID == ""` → `skip reason="empty_model_id"`；
- chat 入口播种 PipelineRequest（session_knowledge_qa.go:118-163）**从不设置 RerankModelID**，
  只有智能体配置可设（session_qa_helpers.go:229）；
- 对照：search-only API（同文件 ~:905-918）有完整解析——租户 RetrievalConfig → 自动选第一个
  可用 rerank 模型。**同一服务两条入口行为不一致**。

后果：租户配置了 rerank 模型，普通知识库问答也从不精排；复合分、MMR、FAQ 加权整条链路
对聊天用户是死代码。**所有池模式历史基线实为纯 RRF 排序。**

### 3.3 请求体白名单（前端脆弱点，upstream issue U-08）

`streame.ts` 按白名单逐字段重建 POST body——后端新增任何请求字段，前端不显式拷贝就在
网络层静默丢失（kb-routing 踩过，修复见 c6721e34 缺陷 1）。

## 4. 二开实现

### 4.1 配置模型（internal/types/retrieval_config.go）

```go
type RankingConfig struct {
    RerankWeight        float64  // 默认 0.6（=旧常量）
    RetrievalWeight     float64  // 默认 0.3
    SourceWeight        float64  // 默认 0.1（同源候选间是常数偏移，不影响排序）
    RecencyWeight       float64  // 默认 0：只铺管线+日志，池语料测不出
    RecencyHalfLifeDays int      // 默认 180：2^(-ageDays/halfLife)
    MMRLambda           float64  // 默认 0.7；1.0=纯相关度
}
```

- 旧常量提升为命名常量（LegacyComposite*），`Resolve()` 把未设字段回落到旧值——
  **on-默认 ≡ off 是构造性保证**（`TestWeightedCompositeMatchesLegacyAtDefaults` 网格遍历验证）；
- `Merge(override)` 逐字段"请求覆盖租户"，nil 安全；
- `RetrievalConfig` 增加 `Ranking *RankingConfig`（JSONB 向后兼容，**无数据库迁移**）。

### 4.2 请求级穿透（照抄 kb-routing 四层范式）

```
CreateKnowledgeQARequest（handler/session/types.go: enable_weighted_ranking + ranking + rerank_model_id）
  → qaRequestContext（qa.go 两处拷贝）
  → types.QARequest（qa_request.go）
  → session_knowledge_qa.go 入口：tenantRanking.Merge(req.Ranking) → PipelineRequest
```

`rerank_model_id` 是独立旋钮：显式模型 ID 或 `"auto"`（复用 search-only 的"第一个可用"
逻辑解析）；空 = 官方旧行为（跳过 rerank）。

### 4.3 打分双路径（chat_pipeline/rerank.go）

- **关闭路径**：`compositeScore()` / `applyMMR(..., 0.7)` 一字未动；
- **开启路径**：`weightedCompositeScore = w_m*model + w_b*base + w_s*source + w_r*recency`，
  λ 取配置；recency 写入 `Metadata["recency"]` 供观测；
- **信号分布日志**（两条路径都打）：每轮一条 `signal_distribution`，含 model/base/recency
  各自 min/p50/p90。

### 4.4 recency 信号来源（零额外查询）

`processSearchResults` 本就批量取回完整 Knowledge 行——在 `buildSearchResult`
（knowledgebase_search_results.go）把 `knowledge.UpdatedAt` 以 RFC3339 盖进
`SearchResult.Metadata["knowledge_updated_at"]`；rerank 阶段解析并按半衰期衰减。

### 4.5 前端「精准排序」按钮

- `flags.ts` 新增 `weightedRankingEnabled()`（`VITE_ENABLE_WEIGHTED_RANKING`，第 5 个二开 flag）；
- Input-field.vue 工具栏按钮（precise-monitor 图标）：仅快速问答可点（`isQuickAnswerMode`
  守卫，智能体模式置灰+换 tooltip）；
- 点亮后请求带 `rerank_model_id:"auto"` + `enable_weighted_ranking:true`
  （后者专为快照持久：后端 `SessionLastRequestState.weighted_ranking_enabled` 落库，
  前端 applyLastRequestState 恢复——这是开发中实测抓到并修掉的状态丢失 bug）；
- streame.ts 白名单显式拷贝两字段（含 params 类型声明）；i18n 四语言，顺带补齐 ko/ru
  的 kb-routing 欠账（审计全绿）。

### 4.6 改动面清单（架构层 → 组件）

| 架构层 | 组件 | 改动 |
|---|---|---|
| 类型/配置 | types/retrieval_config.go | +RankingConfig/Resolve/Merge，RetrievalConfig.Ranking |
| 类型 | types/chat_manage.go、qa_request.go | PipelineRequest/QARequest 各 +2 字段 |
| handler | session/types.go、qa.go | 请求 DTO +3 字段、rc 拷贝、快照 +1 字段 |
| 持久化 | types/session.go | SessionLastRequestState.weighted_ranking_enabled |
| 服务层 | session_knowledge_qa.go | 入口解析 auto/合并权重/播种 |
| 管线插件 | chat_pipeline/rerank.go | 加权路径、λ、recency、分布日志（旧路径不动） |
| 检索装配 | knowledgebase_search_results.go | 盖时间戳 |
| 前端 | flags/store/Input-field/index.vue/streame.ts/4×i18n | 开关全链路 |

**不动**：FILTER_TOP_K（本就只按最终分排序截断）、CHUNK_MERGE、agent 路径公式拷贝
（记录为已知分叉）、任何数据库 schema。

## 5. 官方 vs 二开对比

| 维度 | 官方 | 二开 |
|---|---|---|
| 复合分权重 | 0.6/0.3/0.1 写死 | 四路权重+λ+半衰期，请求>租户>默认 |
| chat 接入 rerank | 从不（静默跳过） | `rerank_model_id`/auto 一键接通 |
| 时效信号 | 无 | 管线就绪，默认权重 0 |
| 信号可观测 | 仅 base/model 落 Metadata | +每轮分布汇总日志 |
| 排序实验能力 | 改源码重编译 | API 传参即实验（harness 已接） |
| UI | 无 | 精准排序按钮（flag 门控） |
| 默认行为 | — | **逐位一致**（单测+臂①实测双证） |

## 6. 测试与有效性讨论

### 6.1 单元测试（rerank_weighted_test.go / ranking_config_test.go）

- 奇偶性：默认权重下新旧公式网格遍历 bit 级相等；
- recency 衰减数学（半衰期=0.5、双倍=0.25、未来=1、异常=0）；
- MMR λ=1.0 退化为纯相关度、λ=0.7 压制近重复；
- 配置解析/Merge 优先级/JSONB 往返。

### 6.2 三臂评测（eval-100 池模式，100 题配对，|Δ|<2 为噪声）

| 指标 | ① off（纯 RRF） | ② +rerank λ0.7 | ③ +rerank λ1.0 |
|---|---|---|---|
| EM / F1 | 47.0 / 65.8 | **53.1 / 71.3** | 47.5 / 65.3 |
| 金标 top3 / top5 / top10 | 70.5/77.0/87.0 | **83.2/90.3/93.4** | 81.8/85.9/89.4 |
| 金标全命中 | 90.0% | **90.8%** | 85.9% |

结论：**接通 rerank 是池模式最大单杠杆**（top3 +12.7、EM +6.1）；**MMR λ=0.7 是功臣**
（关掉回吐 4-6 分——多样性惩罚挤掉的是同文档近重复，护住了多跳题第二篇金标，原假设被推翻）。
信号分布日志进一步揭示权重再平衡是死胡同：RRF 基线分仅 0.008–0.011，rerank 模型分
0.30–0.88，旧公式 0.3×基线项贡献≈0.003。

### 6.3 端到端与手动验证

- 臂①与历史基线噪声带内吻合（老路径未受损的实测证明）；
- GUI 黑盒测试 6 项（5 过 1 环境受阻），DOM+截图+日志+DB 四类证据；
- 用户手测语料内题：只排序臂金标对进引用（Florida Senate 第 4 位）答对 40；双开臂因
  路由选入主题相似的干扰库、小检索池名额被挤而漏金标——单题方差符合统计预期。

### 6.4 诚实的局限

- 单轮 r1（协议要求 r2 复验，+12 级差异不会被翻盘但流程要补）；
- 引用宽度 28.9→11.5 的 confound：top10 对比部分受益于 RerankTopK 截断（top3/top5/EM/F1 不受影响）；
- rerank 每问 +1 次模型调用（成本）；λ 与权重默认值仅在此标尺上验证；
- 遗留：单路检索分数不归一会让复合分饱和（上游 U-02，存量缺陷）。

## 7. 使用方式

```jsonc
// 请求级（评估/联调）
{"query":"...", "rerank_model_id":"auto",                       // 只接 rerank
 "enable_weighted_ranking":true, "ranking":{"mmr_lambda":1.0}}  // +权重实验
// 租户级默认（/tenants/kv/retrieval-config 的 JSONB）
{"ranking":{"rerank_weight":0.6,"retrieval_weight":0.3,"recency_weight":0}}
```
UI：输入工具栏「精准排序」按钮（构建需 `VITE_ENABLE_WEIGHTED_RANKING=1`，现为五 flag）。
观测：`grep -a "weighted_ranking\|signal_distribution" /tmp/weknora-server-wr.log`。

## 8. 已知问题与后续

- agent 路径公式拷贝未接权重（knowledge_search.go，与 U-03 重构一并处理）；
- r2 复验、rerank 是否转 chat 默认（行为变更，待用户拍板）；
- 上游回馈：U-01/U-02/U-03 见 docs/upstream-issues-register.md。
