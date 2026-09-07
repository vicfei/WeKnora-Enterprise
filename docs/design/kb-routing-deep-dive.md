# 深度解析：知识库自动路由（KB Routing）二开实现

> 分支 `feature/kb-routing` @ c6721e34（前几棒交付，第 4 棒在其上继续建设并完成本文）。
> 机制代码：internal/application/service/kb_router.go。本文覆盖：架构速览 → 官方行为分析 →
> 二开设计与代码地图 → 对比 → 测试有效性讨论 → 使用方式 → 已知问题与生产化前置。

## 1. 摘要

官方"快速问答"在用户未手选知识库时，**默认把租户全部知识库都塞进检索范围**（经内置伪智能体
builtin-quick-answer 填充），范围对用户不可见。租户库一多（评估租户 541 个），检索池巨大且
充斥跨题干扰。kb-routing 引入**内容相似度路由**：用问题向量对每个库的文档摘要向量取最大
余弦相似度，选 top-3 库再检索。实测 **top-1 命中率 96%、答案质量与 oracle（手工指定正确库）
持平**，真实 UI 流程端到端验证通过。

## 2. WeKnora 架构速览（路由所经路径）

```
前端 Input-field.vue 城市图标开关（VITE_ENABLE_KB_ROUTING 门控）
  └→ streame.ts 白名单拷贝 enable_kb_routing（曾静默丢失，已修）
       └→ SSE POST /api/v1/knowledge-chat/:id
            handler/session/qa.go：请求 DTO → QARequest.EnableKBRouting
             服务层 session_knowledge_qa.go:92 门控（唯一决策点）：
               req.EnableKBRouting && 无显式 KB/文件/标签范围
                 → routeKnowledgeBases()（kb_router.go，同步调用）
                     ↳ 选出的库替换 knowledgeBaseIDs 继续常规管线
                     ↳ emitKBRoutingEvent → SSE tool_call 事件（前端/评估可消费）
             之后照常：CHUNK_SEARCH_PARALLEL → RERANK → MERGE → FILTER_TOP_K → LLM
```

关键架构事实：**路由不是管线插件**，而是服务层入口处的一次前置决策——它在管线触发前
改写 `knowledgeBaseIDs`，对下游完全透明（这是它能与任何检索/排序特性正交叠加的原因，
第 4 棒的"双开"手测即叠加态）。

## 3. 官方实现分析

官方没有"路由"概念，相关行为散落三处：

1. **范围默认值**：builtin-quick-answer 伪智能体（前端 BUILTIN_QUICK_ANSWER_ID）默认填入
   全部知识库 → 未选库=全库检索，无任何提示（upstream U-09）；
2. **search-only API**（GET /api/v1/knowledge-bases/search 类接口）支持显式传 kb_ids，
   但聊天链路的请求 DTO 只有范围类参数（knowledge_base_ids/knowledge_ids），无任何
   "自动缩小范围"的机制；
3. **前端选择器**：工具栏"知识库"弹层是浏览型（点卡片=跳详情页），用户缺少把单库
   选进对话范围的便捷入口——进一步放大了"全库默认"的影响面。

多库检索本身官方支持（HybridSearch 按 KB 分组并行），但候选集越大队干扰越强——
这正是路由要解决的问题，也是它能在池模式标尺上显现价值的原因。

## 4. 二开实现

### 4.1 核心算法（kb_router.go:106-223）

```
候选集   = 租户全部 KB（v1：不做用户可见性过滤——代码内有警告注释）
预处理   = 按嵌入模型分组，只保留"多数派模型组"（跨模型向量不可比）
质心缓存 = kbCentroidCache（TTL 默认 15 分钟，KB_ROUTER_CACHE_TTL_MIN 可调）
           每个 KB 的文档摘要（docDigest：标题+首段截断）批量嵌入（BatchEmbed）
打分     = cosineSimilarity(问题向量, 各文档摘要向量) 取最大值 = KBScore
选择     = 降序排序，取 top-K（默认 3，KB_ROUTER_TOP_K），低于 minScore（默认 0.25，
           KB_ROUTER_MIN_SCORE）的丢弃但**永远保留 top-1**
外发     = emitKBRoutingEvent：SSE tool_call 事件 kb_router，含各库得分（可观测）
```

问题向量复用检索层的 `GetQueryEmbedding`——与正式检索同一模型同一缓存，不额外调用。

### 4.2 门控与显式选择优先

session_knowledge_qa.go:92 的门控条件保证路由**永不覆盖显式选择**：请求带
knowledge_base_ids / knowledge_ids / @提及标签范围时，路由直接不介入。评估与手测均验证。

### 4.3 请求级开关与持久化（四层穿透）

```
CreateKnowledgeQARequest.enable_kb_routing（handler/session/types.go:55）
  → qaRequestContext（qa.go）→ QARequest（qa_request.go:22）
  → 服务层门控 → SessionLastRequestState.kb_routing_enabled（会话快照，qa.go:1426）
前端：Input-field.vue 城市按钮（仅快速问答可点）→ streame.ts 白名单 →
      settings store toggleKbRouting（开启时清空手选库，与手选互斥）→ 快照恢复
```

### 4.4 开发中修掉的三个缺陷（c6721e34，真实 UI 测试发现）

1. **streame.ts 白名单丢字段**：enable_kb_routing 在网络层静默蒸发（U-08 的直接受害者）；
2. **路由前提永假**：builtin-quick-answer 默认填全部库，"未手选库"的后端判定永远不成立——
   修复为该伪智能体的全库填充不视为显式选择；
3. **开关不持久**：未随 SessionLastRequestState 快照，跨页面导航即丢。

这三个坑是所有"请求级二开开关"的通用雷区，第 4 棒的精准排序按钮逐条照方抓药规避。

### 4.5 改动面清单

| 架构层 | 组件 | 改动 |
|---|---|---|
| 服务层 | kb_router.go（新文件 ~350 行） | 路由算法+缓存+SSE 事件+env 旋钮 |
| 服务层 | session_knowledge_qa.go | 入口门控+库列表改写 |
| handler | session/qa.go、types.go | 请求字段+快照字段 |
| 类型 | qa_request.go、session.go | QARequest/快照 +字段 |
| 前端 | flags/store/Input-field/index.vue/streame.ts/i18n | 城市按钮全链路 |
| **不动** | 检索/排序/合并管线、HybridSearch | 零改动（正交设计） |

## 5. 官方 vs 二开对比

| 维度 | 官方 | 二开 kb-routing |
|---|---|---|
| 未选库时范围 | 全租户库（不可见） | 按内容相似度 top-3（0.25 分阈） |
| 范围决策可观测 | 无 | SSE kb_router 事件（含各库得分） |
| 显式选择 | 支持 | 支持，且**永远优先**（路由不介入） |
| 范围可控性 | 仅全库或手选 | 三档：手选 > 路由 > 全库 |
| 调参 | — | env：TOP_K / MIN_SCORE / CACHE_TTL |

## 6. 测试与有效性讨论

### 6.1 批量评测（eval-100，路由模式 vs oracle 指定正确库）

- **top-1 命中 96%、top-3 97%**（routing-v11 报告，:5173/report-routing-v11-100.html）；
- 答案质量与 oracle 持平：EM +1.0 / F1 −1.3，均在 ±2 噪声带内——**路由选库不丢答案质量**；
- 检索层指标（金标命中）显著优于全库基线（缩小范围直接压缩干扰面）。

### 6.2 真实 UI 端到端（用户亲手 + 自动化双验证）

- 用户浏览器实测通过（第 3 棒）；第 4 棒无头 Chromium 复验登录→开关→提问链路正常；
- 用户手测（Ken Pruitt 题）：路由两次独立运行选中**同一批 3 库、同分 0.693/0.676**——
  决策确定性实测确认。

### 6.3 有效的边界（何时会输）

- **主题撞车的干扰库**：路由按"库级内容相似"选库，其他题的库若整体主题与题面词高度
  相似（如多个"立法机构"库），会一起入选并挤占小检索池名额——用户手测中"双开"臂因此
  漏掉金标而只排序臂答对。这是 top-K 质心相似度的固有权衡，评估的 96% 是 100 题统计，
  单题方差真实存在；
- 冷缓存首问 36–38s（541 库质心批量嵌入；实测还引发阿里云 429 连环，upstream U-05）；
- 候选集不过滤用户可见性（v1 租户全量，代码有警告注释）——多租户生产化前必须修。

### 6.4 与加权排序的协同

路由（范围层）与 rerank（排序层）正交：路由缩小候选面降低干扰基数，rerank 在候选内
精排。手测中"双开"臂翻车属单题方差而非机制冲突；统计上两特性可各自独立开启、独立评估
（各自的 on/off 配对实验都做了）。

## 7. 使用方式

```jsonc
// API：不选库 + 开路由（显式选择时不生效）
{"query":"...", "enable_kb_routing": true}   // knowledge_base_ids 留空
```
UI：城市图标（构建需 `VITE_ENABLE_KB_ROUTING=1`；仅快速问答可点，智能体模式置灰）。
观测：SSE `tool_call` 事件 `kb_router`（selected 列表含得分）；日志
`grep -a "query routed" /tmp/weknora-server-*.log`。调参：三个 env 见 4.1。

## 8. 已知问题与生产化前置

1. **候选库可见性过滤**（必做：v1 是租户全量，未按用户权限过滤）；
2. 冷缓存慢 + 无持久化（U-12）：需预热任务或向量缓存落盘；批量嵌入需退避节流（U-05）；
3. agent 链路未接入（待办：从 rag-qa 类智能体做起，候选=agent 允许库范围）；
4. minScore/topK 默认值仅在评估租户标定，生产租户分布不同需重新观察信号再调。
