# 深度设计：时效感知检索（Time-Aware Retrieval）

> 状态：**设计定稿，待实施**（前置条件：六分支栈落顶后从集成分支切 `feature/time-aware-retrieval`）。
> 定位：检索三部曲的第三部——选对库（kb-routing，已完成）→ 排好序（weighted-ranking，已完成）→ **答最新（本文）**。
> 所有代码坐标基于 feature/web-search-providers @ 4f6e4274 实测核验（2026-09-16）。

## 1. 背景与目标

信评/资管场景的最高危失败模式是**静默时效错误**：用户问"XX 公司的评级"，系统把 2024 年的
旧评级当现值答出去——不报错、不拒答，但答案已过期。这类错误在合规语境下比"答不出"严重
一个量级。当前检索栈对时间完全无感知：查询理解不解析时间意图，检索层无时间过滤，排序信号
虽已埋线但默认惰性。

**目标**（按问题类型覆盖）：

| 类型 | 问题形态 | 本设计覆盖方式 |
|---|---|---|
| 1 显式窗口 | "近三个月的评级报告" | time_condition 硬过滤 |
| 2 隐式时效 | "XX 的当前评级"（未说时间） | 惯性默认窗口 + recency 加权 + 答案侧标注数据日期 |
| 3 时点查询 | "截至 2026-06-30 的持仓" | time_condition(lte) |
| 6/7/8 对比/趋势/最新 | "最新一期年报" 等 | 过滤 + recency 排序联合 |

（编号沿用信评问题分类口径；类型 4 范围统计、5 事件时序不在本设计范围。）

**非目标**：事件图谱抽取、文档版本链、跨文档时间推理——已有独立评估结论（暂缓）。

## 2. 现状与差距（代码事实）

**已就位的资产**（本设计的杠杆点）：

1. **白名单范式现成**：tag 体系已实现"条件→knowledge 白名单"——`SearchParams.TagIDs`
   （types/search.go:246）在文档型 KB 上经 `resolveDocumentTagScope`
   （knowledgebase_search.go:306）解析为 `KnowledgeIDs` 白名单；**空解析=空结果而非
   全库回退**（语义已写进字段注释）。FAQ 型走索引层过滤的分流逻辑也现成（:319-326）。
2. **排序信号已埋线且惰性**：`RecencySignal`（searchutil/ranking.go:44）已实现指数衰减
   （半衰期可配），读 `SearchResult.Metadata["knowledge_updated_at"]`
   （注入点 knowledgebase_search_results.go:342）；`ResolvedRanking.RecencyWeight`
   **默认 0**（retrieval_config.go:39-41 注释明示"plumbed but inert"）——信号管道全线贯通，
   只差有意义的信号源和权重。
3. **元数据基础设施已有**：`Knowledge.CustomMetadata`（types/knowledge.go:180，JSON 列，
   已渲染进检索结果 knowledgebase_search_results.go:330）。
4. **查询理解有结构化输出口**：`queryUnderstandOutput`（chat_pipeline/query_understand.go:28）
   已输出 RewriteQuery/Intent/ImageDescription 三字段，加字段是自然延伸。
5. **agent 侧共享参数构建**：knowledge_search 工具从 SearchTarget 组装 SearchParams
   （agent/tools/knowledge_search.go:566-611），新字段天然可透传。

**缺口**（本设计要补的）：

- 无业务时间字段（knowledge 只有系统 updated_at，≠ 文档业务时点）
- 查询理解不产时间条件
- 检索层无时间解析/过滤
- agent 工具无时间参数
- 无时效类评估标尺

## 3. 总体设计

```
用户提问（含时间意图）
  │
  ▼
①查询理解（chat_pipeline/query_understand）
  queryUnderstandOutput += TimeCondition{field,op,value(s),strictness}
  │                                    （相对时间→绝对时间，服务器时区解析）
  ▼
②检索装配（SearchParams += TimeCondition）
  │
  ▼
③范围解析（新 resolveDocumentTimeScope，镜像 tag 范式）
  文档型 KB：business_date 范围 → KnowledgeIDs 白名单（与 tag 白名单求交）
  FAQ 型：knowledge 级 updated_at 过滤（业务时间不适用，降级语义显式记录）
  空解析=空结果（沿用 tag 语义，防静默回退全库）
  │
  ▼
④排序（现有 CompositeScore，零新字段）
  RecencySignal 信号源升级：business_date 优先，updated_at 兜底
  权重沿用 RankingConfig.RecencyWeight（默认 0 保持惰性，显式开启）
  │
  ▼
⑤答案侧（不改）
  引用已带文档元数据 → 答案标注"数据日期"的提示由 rerank/merge 既有机制渲染
```

**核心原则**：**默认零行为变更**——无 TimeCondition 时整条链路不激活；RecencyWeight 默认 0
不变。这让我们可以安全合入、渐进开启（与 weighted-ranking 的上线纪律同构）。

## 4. 分模块详细设计

### 4.1 数据层：business_date 字段

**决策 D1：一等列，不骑 CustomMetadata。** 理由：范围查询（gte/lte/between）需要索引，
JSON 列上的表达式索引在迁移和多库兼容上成本更高；且业务时间是要参与查询语义的字段，
不是展示型元数据。

- `knowledges` 表新增 `business_date TIMESTAMPTZ NULL`（fork 迁移 000087，接续 000084-86 惯例）；
  普通索引 `idx_knowledges_business_date`（复合 `(knowledge_base_id, business_date)` 视实测计划定）。
- **不回填**：存量行保持 NULL——数据层不制造"系统时间冒充业务时间"的假象。
- 写入路径：知识创建/更新 API 增可选 `business_date` 字段（manual 灌库与上传 API 同步加）；
  **治理前提**（与信评方约定）：am-qa 语料入库必填，缺失时按 KB 级配置决定拒绝或告警
  （`KnowledgeBase` 配置加 `require_business_date bool`，默认 false）。
- 解析侧默认值（二期可选）：docreader 提取文档发布日期回填——仅在显式置信时写入。

### 4.2 类型层：SearchTimeCondition

```go
// types/search.go，SearchParams 同文件
type SearchTimeCondition struct {
    Field      string    `json:"field"`                 // "business_date" | "updated_at"
    Op         string    `json:"op"`                    // "gte" | "lte" | "between"
    Start, End time.Time // between 用双值；gte/lte 用 Start
    Strictness string    `json:"strictness,omitempty"`  // "filter"(默认) | "prefer"
}
// SearchParams += TimeCondition *SearchTimeCondition `json:"time_condition,omitempty"`
```

**决策 D2：Strictness 双档。** `filter`=硬过滤（显式时间窗，空结果诚实返回）；
`prefer`=不排除只调序（模糊时效词如"最新的"，由 recency 加权表达，防硬过滤误杀）。
查询理解层负责判断该用哪档——显式窗口/时点→filter，"最新/近期"模糊词→prefer。
这是防"过滤过狠导致零结果"的安全阀。

### 4.3 查询理解层

- `queryUnderstandOutput` 增 `TimeCondition *types.SearchTimeCondition`；
  提示词（query_understand 模板）增时间抽取指令与 few-shot（信评向样例：
  "近三个月"→相对解析、"截至去年年报"→年度边界、"最新"→prefer 档不产窗口）。
- **相对时间解析纪律**：以**服务器时间（Asia/Shanghai）**为基准解析，输出一律绝对时间；
  意图不可判时不产 TimeCondition（宁可无时间不可错时间——错窗口比无窗口危害更大，
  与"空解析不回退全库"同理）。
- 输出流入检索：与 RewriteQuery 同路透传至 SearchParams（chat 管道既有管线）。

### 4.4 检索层：resolveDocumentTimeScope

镜像 `resolveDocumentTagScope`（knowledgebase_search.go:306）新建：

```
输入：kbs, params.TimeCondition
分流：
  - 全文档型：查 knowledges WHERE kb_id IN (...) AND business_date ∈ 窗口
    → KnowledgeIDs 白名单（与既有 KnowledgeIDs/tag 白名单求交——交集为空即空结果）
  - 含 FAQ：FAQ 无业务时间 → 对 FAQ 部分按 updated_at 降级过滤（knowledge 级），
    日志记 time_scope_fallback=updated_at（观测可见）
  - Field=updated_at：直接按系统时间过滤（免治理依赖的兜底通道）
```

- 空窗口命中：**空结果 + 响应事件带 `time_filter: {applied: true, matched: 0}`**
  （复用 U-06 的"降级可见性"思想——静默失败是本设计要消灭的，不能自己引入）。
- 十个检索引擎仓库层**零改动**（时间在服务层收敛为白名单，与 tag 同构——这是选白名单
  范式而非索引层过滤的根本原因）。

### 4.5 排序层：信号源升级

- 注入点（knowledgebase_search_results.go:342 旁）增写两个元数据键：
  `knowledge_business_date`（有则写）与 `knowledge_time_source`（"business"|"system"，
  标记信号来源，可观测）。
- `RecencySignal` 调用点两处（chat_pipeline/rerank.go:507、agent knowledge_search.go:1268）
  改为：business_date 存在则用之，否则回退 updated_at。函数本身不改（纯输入升级）。
- **权重零新增字段**：沿用 RankingConfig.RecencyWeight + RecencyHalfLifeDays。
  实施后建议 am-qa 语料上做 weight 0/0.05/0.1 三臂（复用 weighted-ranking 评估骨架）。

### 4.6 agent 侧（二期，M2）

knowledge_search 工具 schema 增可选参数 `time_min` / `time_max`（YYYY-MM-DD）：
- 模型从用户话术提取（系统提示加一行指引："用户提及时间范围/时点时传入"）；
- 服务端组 TimeCondition 走同一 resolveDocumentTimeScope——**与 chat 同一条解析路径**，
  避免 U-03 之前那种双路径漂移。
- 不做自然语言参数（"最近"）：让模型给绝对值，服务端只认结构化——错误面更小。

### 4.7 前端与请求透传（三期，M3 可选）

若要 UI 开关/展示（时间过滤 chip、答案侧"数据日期"徽标）：沿用 kb-routing 三坑纪律
（streame.ts 白名单 + SessionLastRequestState 快照 + 构建后双自检）。API-only 阶段不需要。

## 5. 评估方案（新标尺：evals/time）

- **语料**：合成确定性语料（seed 固定）——~200 篇文档铺在 24 个月的时间轴上（内容含
  明确日期锚点），gold 标注每篇的 business_date。
- **题集**：60 题三型各 20——显式窗口（"2026 年 Q1 的…"）/ 时点（"截至 X 的…"）/
  隐式时效（"当前的…"，gold=最新窗口内文档）。
- **指标**：
  - **窗口精确率**（命中文档在窗口内占比）——过滤正确性；
  - **过期引用率**（答案引用窗口外文档且未标注日期）——**合规核心指标**，目标 0；
  - 常规 EM/F1 作对照。
- **跑法**：on/off 配对（time-aware 开/关），|Δ| 判据沿用评估纪律；报告进 ：5173 索引。

## 6. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 查询理解错抽时间（把"三年期债券"当时间窗） | few-shot 反例 + 不可判不产出 + strictness=prefer 兜底 |
| 硬过滤零结果伤体验 | prefer 档 + 空结果事件显式可见（不静默） |
| 存量语料无 business_date | 数据层不回填假值；updated_at 兜底通道 + 治理要求新语料必填 |
| 与 upstream 合并冲突 | 新文件为主 + 五个小接缝（SearchParams/查询理解/注入点/两调用点），冲突面可控 |
| agent 与 chat 双路径漂移 | 同一 resolveDocumentTimeScope，单点实现（U-03 教训） |

## 7. 实施计划（合计约 8-9 人日）

| 里程碑 | 内容 | 工作量 |
|---|---|---|
| M1 核心（chat 链） | 迁移+字段+API、SearchTimeCondition、resolveDocumentTimeScope+单测、查询理解抽取、排序信号升级、管道透传 | 5 人日 |
| M2 agent 链 | knowledge_search 工具参数+系统提示+同路径解析 | 1 人日 |
| M3 标尺与报告 | 合成语料+题集+跑批+报告 | 2-3 人日 |

**分支策略**：从落顶后的集成分支切 `feature/time-aware-retrieval`；M1 落地即可合回
（默认惰性零风险），M2/M3 各自独立提交。

**验收清单**（实施 Definition of Done）：
1. 无时间意图的 100 题回归：与基线全指标噪声带内（零行为变更证明）；
2. 显式窗口题：窗口精确率 ≥95%，过期引用率 0；
3. 隐式时效题（prefer 档）：过期引用率显著下降且 EM 不降超 2 分；
4. FAQ 混合库：降级路径有日志与事件标注；
5. am-qa 真实语料抽样 20 题人工核验（数据日期标注正确）。
